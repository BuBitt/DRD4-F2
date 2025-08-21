package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"drd4/internal/fasta"
	"drd4/internal/ncbi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Simple TUI: left/main area shows a selectable list of FASTA headers and
// their sequences; the right column shows a reference sequence.

type model struct {
	records            []fasta.FastaRecord
	cursor             int
	width              int
	height             int
	refIdx             int // index of reference record in records (-1 if none)
	status             string
	mu                 sync.Mutex
	headerOffset       int
	sequenceOffset     int
	targetHeaderOffset int
	animating          bool
}

func initialModel(recs []fasta.FastaRecord, refIdx int) *model {
	return &model{records: recs, cursor: 0, refIdx: refIdx}
}

func (m *model) Init() tea.Cmd { return nil }

type tickMsg struct{}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.records)-1 {
				m.cursor++
				// ensure cursor visible in header viewport (may animate)
				return m, m.ensureCursorVisible()
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
				return m, m.ensureCursorVisible()
			}
		case " ":
			// page down sequence
			m.pageSequence(1)
		case "b":
			// page up sequence
			m.pageSequence(-1)
		case "g":
			m.cursor = 0
			return m, m.ensureCursorVisible()
		case "G":
			m.cursor = len(m.records) - 1
			return m, m.ensureCursorVisible()
		case "a":
			// run MAFFT on the input file (non-blocking)
			go func() {
				m.mu.Lock()
				m.status = "running mafft..."
				m.mu.Unlock()
				if p, err := exec.LookPath("mafft"); err == nil {
					cmd := exec.Command(p, "--auto", "-")
					// no stdin provided; this is a placeholder
					_ = cmd.Start()
					_ = cmd.Process.Release()
					time.Sleep(500 * time.Millisecond)
				}
				m.mu.Lock()
				m.status = "mafft started"
				m.mu.Unlock()
			}()
		case "f":
			// fetch translation for current accession via NCBI (non-blocking)
			go func(idx int) {
				acc := ""
				if idx >= 0 && idx < len(m.records) {
					fields := strings.Fields(m.records[idx].Header)
					if len(fields) > 0 {
						acc = fields[0]
					}
				}
				if acc == "" {
					m.mu.Lock()
					m.status = "no accession found for record"
					m.mu.Unlock()
					return
				}
				m.mu.Lock()
				m.status = "fetching translation for " + acc
				m.mu.Unlock()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				res, err := ncbi.FetchTranslations(ctx, []string{acc})
				if err != nil {
					m.mu.Lock()
					m.status = "ncbi fetch error: " + err.Error()
					m.mu.Unlock()
					return
				}
				if t, ok := res[acc]; ok {
					m.mu.Lock()
					m.status = "translation fetched (len=" + fmt.Sprint(len(t)) + ")"
					m.mu.Unlock()
				} else {
					m.mu.Lock()
					m.status = "no translation found"
					m.mu.Unlock()
				}
			}(m.cursor)
		case "s":
			// save current record as JSON
			go func(idx int) {
				if idx < 0 || idx >= len(m.records) {
					m.mu.Lock()
					m.status = "invalid record index"
					m.mu.Unlock()
					return
				}
				rec := m.records[idx]
				b, _ := json.MarshalIndent(rec, "", "  ")
				fname := fmt.Sprintf("record-%d.json", idx)
				if err := os.WriteFile(fname, b, 0o644); err != nil {
					m.mu.Lock()
					m.status = "save failed: " + err.Error()
					m.mu.Unlock()
					return
				}
				m.mu.Lock()
				m.status = "saved " + fname
				m.mu.Unlock()
			}(m.cursor)
		}
	case tickMsg:
		// animate headerOffset towards targetHeaderOffset
		if m.animating {
			if m.headerOffset < m.targetHeaderOffset {
				m.headerOffset++
			} else if m.headerOffset > m.targetHeaderOffset {
				m.headerOffset--
			}
			if m.headerOffset == m.targetHeaderOffset {
				m.animating = false
				return m, nil
			}
			// continue animation
			return m, tea.Tick(time.Millisecond*20, func(t time.Time) tea.Msg { return tickMsg{} })
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

// (removed older ensureCursorVisible implementation; new animated version defined below)

// pageSequence moves the sequence viewport by one page; dir=1 page down, dir=-1 page up
func (m *model) pageSequence(dir int) {
	// compute headerHeight similar to renderMain
	headerHeight := int(float64(m.height) * 0.35)
	if headerHeight < 3 {
		headerHeight = 3
	}
	maxHeader := m.height - 6
	if maxHeader < 3 {
		maxHeader = 3
	}
	if headerHeight > maxHeader {
		headerHeight = maxHeader
	}
	// available lines for sequence roughly height - headerHeight - footer
	footerLines := 2
	seqLines := m.height - headerHeight - footerLines - 4
	if seqLines < 3 {
		seqLines = 3
	}
	if dir > 0 {
		m.sequenceOffset += seqLines
	} else {
		m.sequenceOffset -= seqLines
	}
	if m.sequenceOffset < 0 {
		m.sequenceOffset = 0
	}
}

// ensureCursorVisible adjusts headerOffset so the cursor falls within the visible header window
// and returns a tea.Cmd that animates the header offset when needed.
func (m *model) ensureCursorVisible() tea.Cmd {
	// compute header area height as proportion of terminal height, clamped
	headerHeight := int(float64(m.height) * 0.35)
	if headerHeight < 3 {
		headerHeight = 3
	}
	maxHeader := m.height - 6
	if maxHeader < 3 {
		maxHeader = 3
	}
	if headerHeight > maxHeader {
		headerHeight = maxHeader
	}
	// center cursor in viewport when possible for better visibility
	desired := m.cursor - headerHeight/2
	if desired < 0 {
		desired = 0
	}
	maxOffset := 0
	if len(m.records) > headerHeight {
		maxOffset = len(m.records) - headerHeight
	}
	if desired > maxOffset {
		desired = maxOffset
	}
	if desired == m.headerOffset {
		return nil
	}
	m.targetHeaderOffset = desired
	m.animating = true
	return tea.Tick(time.Millisecond*20, func(t time.Time) tea.Msg { return tickMsg{} })
}

func renderMain(m *model, mainWidth int) string {
	var b strings.Builder
	b.WriteString("Headers:\n")
	// determine header viewport
	headerHeight := m.height / 3
	if headerHeight < 3 {
		headerHeight = 3
	}
	start := m.headerOffset
	end := start + headerHeight
	if end > len(m.records) {
		end = len(m.records)
	}
	// indicate truncated top
	if start > 0 {
		b.WriteString("...\n")
	}
	for i := start; i < end; i++ {
		r := m.records[i]
		marker := " "
		if i == m.cursor {
			marker = ">"
		}
		line := r.Header
		if len(line) > mainWidth-6 {
			line = line[:mainWidth-9] + "..."
		}
		fmt.Fprintf(&b, "%s %s\n", marker, line)
	}
	if end < len(m.records) {
		b.WriteString("...\n")
	}
	b.WriteString("\nSequence:\n")
	if mainWidth < 4 {
		mainWidth = 4
	}
	if m.cursor >= 0 && m.cursor < len(m.records) {
		seq := m.records[m.cursor].Sequence
		// wrap to width (avoid infinite loop)
		for i := 0; i < len(seq); i += mainWidth {
			end := i + mainWidth
			if end > len(seq) {
				end = len(seq)
			}
			b.WriteString(seq[i:end] + "\n")
		}
	}
	return b.String()
}

func renderRef(m *model, refWidth int) string {
	var b strings.Builder
	b.WriteString("Reference\n\n")
	if refWidth < 4 {
		refWidth = 4
	}
	if m.refIdx >= 0 && m.refIdx < len(m.records) {
		r := m.records[m.refIdx]
		b.WriteString(r.Header + "\n\n")
		seq := r.Sequence
		for i := 0; i < len(seq); i += refWidth {
			end := i + refWidth
			if end > len(seq) {
				end = len(seq)
			}
			b.WriteString(seq[i:end] + "\n")
		}
	} else {
		b.WriteString("(no reference found)\n")
	}
	return b.String()
}

func (m *model) View() string {
	gap := 2
	// Require a valid terminal size
	if m.width <= 0 || m.height <= 0 {
		return "Waiting for terminal size..."
	}

	// compute reference column as 30% of width, clamped
	refWidth := int(float64(m.width) * 0.3)
	if refWidth < 20 {
		refWidth = 20
	}
	// ensure refWidth leaves space for main column
	minMain := 20
	if refWidth > m.width-minMain-gap {
		refWidth = m.width - minMain - gap
		if refWidth < 4 {
			refWidth = 4
		}
	}
	mainWidth := m.width - refWidth - gap
	if mainWidth < minMain {
		mainWidth = minMain
		if mainWidth > m.width-gap {
			mainWidth = m.width - gap
		}
	}

	mainStyle := lipgloss.NewStyle().Width(mainWidth).Padding(1).Border(lipgloss.NormalBorder())
	refStyle := lipgloss.NewStyle().Width(refWidth).Padding(1).Border(lipgloss.NormalBorder()).Align(lipgloss.Center)

	// leave padding inside styles; rendering widths subtract padding
	main := renderMain(m, mainWidth-4)
	ref := renderRef(m, refWidth-4)
	status := ""
	m.mu.Lock()
	status = m.status
	m.mu.Unlock()
	footer := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("a: run mafft • f: fetch translation • s: save record • q: quit   " + status)

	return lipgloss.JoinHorizontal(lipgloss.Top, mainStyle.Render(main), lipgloss.NewStyle().Width(gap).Render(""), refStyle.Render(ref)) + "\n" + footer
}

func main() {
	inFlag := flag.String("in", "drd4-tdah.fasta", "input FASTA file")
	refFlag := flag.String("ref", "", "reference header to display on the right column (substring match)")
	flag.Parse()

	f, err := os.Open(*inFlag)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "input file not found: %s\n", *inFlag)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "failed to open input: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	// Parse FASTA
	records := fasta.ParseFasta(f)
	if len(records) == 0 {
		fmt.Fprintln(os.Stderr, "no FASTA records found")
		os.Exit(1)
	}

	// find reference by substring match if provided, else try to find the NM_000797 accession
	refIdx := -1
	if *refFlag != "" {
		for i, r := range records {
			if strings.Contains(r.Header, *refFlag) {
				refIdx = i
				break
			}
		}
	}
	if refIdx == -1 {
		for i, r := range records {
			if strings.HasPrefix(r.Header, "NM_000797.4") || strings.Contains(r.Header, "DRD4") {
				refIdx = i
				break
			}
		}
	}

	m := initialModel(records, refIdx)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if err := p.Start(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "failed to start TUI: %v\n", err)
		os.Exit(1)
	}
}
