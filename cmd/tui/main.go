package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Colors for modern design
var (
	primaryColor   = lipgloss.Color("#7C3AED") // Purple
	secondaryColor = lipgloss.Color("#10B981") // Green
	accentColor    = lipgloss.Color("#F59E0B") // Amber
	surfaceColor   = lipgloss.Color("#1F2937") // Dark gray
	textColor      = lipgloss.Color("#F3F4F6") // Light gray
	mutedColor     = lipgloss.Color("#9CA3AF") // Muted gray
	borderColor    = lipgloss.Color("#374151") // Border gray
)

// Styles
var (
	containerStyle = lipgloss.NewStyle().
			Padding(0, 1).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor)

	titleStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true).
			Align(lipgloss.Center)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(textColor).
			Background(surfaceColor).
			Padding(0, 1)

	sequenceStyle = lipgloss.NewStyle().
			Foreground(textColor).
			Background(lipgloss.Color("#111827")).
			Padding(1).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(borderColor)
	// Source styles
	sourceNCBIStyle    = lipgloss.NewStyle().Foreground(secondaryColor).Bold(true)
	sourceSeqkitStyle  = lipgloss.NewStyle().Foreground(accentColor).Bold(true)
	sourceUnknownStyle = lipgloss.NewStyle().Foreground(mutedColor)
)

type DRD4Record struct {
	Name              string `json:"name"`
	VariantCode       string `json:"variant_code"`
	Nucleotides       string `json:"nucleotides"`
	Translated        string `json:"translated"`
	NucleotidesAlign  string `json:"nucleotides_align"`
	PBCount           int    `json:"pb_count,omitempty"`
	AACount           int    `json:"aa_count,omitempty"`
	TranslationSource string `json:"translation_source,omitempty"`
}

type listItem struct {
	record DRD4Record
}

func (i listItem) FilterValue() string {
	return i.record.VariantCode
}

func (i listItem) Title() string {
	// Title should show only the variant code
	if i.record.VariantCode != "" {
		return i.record.VariantCode
	}
	// fallback to name when code is missing
	return i.record.Name
}

func (i listItem) Description() string {
	// Metadata line shown below the title in the selector list
	src := i.record.TranslationSource
	if src == "" {
		src = "unknown"
	}
	var srcRendered string
	switch src {
	case "ncbi":
		srcRendered = sourceNCBIStyle.Render(src)
	case "seqkit":
		srcRendered = sourceSeqkitStyle.Render(src)
	default:
		srcRendered = sourceUnknownStyle.Render(src)
	}
	return fmt.Sprintf("Source: %s    PB: %d    AA: %d", srcRendered, i.record.PBCount, i.record.AACount)
}

type mode int

const (
	modeNucleotides mode = iota
	modeTranslated
	modeAlignment
)

func (m mode) String() string {
	switch m {
	case modeNucleotides:
		return "🧬 Nucleotides"
	case modeTranslated:
		return "🧪 Translated"
	case modeAlignment:
		return "📐 Alignment"
	default:
		return "Unknown"
	}
}

type model struct {
	list          list.Model
	records       []DRD4Record
	currentMode   mode
	showHelp      bool
	width         int
	height        int
	totalRecords  int
	selectedIndex int
	// right panel focus/scroll state
	rightFocused bool
	rightScroll  int
}

func initialModel() model {
	// Load data
	data, err := os.ReadFile("database.json")
	if err != nil {
		log.Fatal(err)
	}

	var records []DRD4Record
	if err := json.Unmarshal(data, &records); err != nil {
		log.Fatal(err)
	}

	// Create list items
	items := make([]list.Item, len(records))
	for i, record := range records {
		items[i] = listItem{record: record}
	}

	// Create list
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "DRD4 Variants"
	l.SetShowStatusBar(false)
	l.SetShowPagination(true)
	l.SetFilteringEnabled(true)

	return model{
		list:         l,
		records:      records,
		currentMode:  modeNucleotides,
		totalRecords: len(records),
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Calculate list dimensions (left panel takes 1/3 of width)
		listWidth := msg.Width / 3
		listHeight := msg.Height - 4 // Account for borders and status

		m.list.SetWidth(listWidth)
		m.list.SetHeight(listHeight)

		return m, nil

	case tea.MouseMsg:
		// Basic mouse support: left click on right panel focuses it. Use newer MouseAction/MouseButton API.
		x := msg.X
		listWidth := m.width / 3
		if x >= listWidth {
			text := msg.String()
			// crude detection for left click release in different bubbletea versions
			if strings.Contains(strings.ToLower(text), "left") || strings.Contains(strings.ToLower(text), "mouseleft") {
				m.rightFocused = true
				// approximate scroll based on click Y so user lands near clicked position
				headerHeight := 4
				newScroll := msg.Y - headerHeight - 2
				if newScroll < 0 {
					newScroll = 0
				}
				m.rightScroll = newScroll
			}
			return m, nil
		}

	case tea.KeyMsg:
		k := msg.String()
		// When right panel is focused, intercept up/down/left/right for scrolling
		if m.rightFocused {
			switch k {
			case "up", "k":
				if m.rightScroll > 0 {
					m.rightScroll--
				}
				return m, nil
			case "down", "j":
				// compute max scroll using dynamically built lines from selected record
				sel := m.list.SelectedItem()
				max := 0
				if sel != nil {
					rec := sel.(listItem).record
					lines := m.buildRightLines(rec)
					if len(lines) > 0 {
						// header+mode+meta+blank take 4 lines in the focused panel
						headerLines := 4
						visibleSeqRows := (m.height - 6) - headerLines
						if visibleSeqRows < 1 {
							visibleSeqRows = 1
						}
						max = len(lines) - visibleSeqRows
						if max < 0 {
							max = 0
						}
					}
				}
				if m.rightScroll < max {
					m.rightScroll++
				}
				return m, nil
			case "pgup":
				// page up
				page := m.height - 6
				if page <= 0 {
					page = 10
				}
				m.rightScroll -= page
				if m.rightScroll < 0 {
					m.rightScroll = 0
				}
				return m, nil
			case "pgdown":
				page := m.height - 6
				if page <= 0 {
					page = 10
				}
				m.rightScroll += page
				return m, nil
			case "left":
				m.rightFocused = false
				return m, nil
			}
		}

		switch k {
		case "ctrl+c", "q":
			return m, tea.Quit

		case "h":
			m.showHelp = !m.showHelp
			return m, nil

		case "1":
			m.currentMode = modeNucleotides
			return m, nil

		case "2":
			m.currentMode = modeTranslated
			return m, nil

		case "3":
			m.currentMode = modeAlignment
			return m, nil

		case "right":
			// focus right panel and reset scroll
			m.rightFocused = true
			m.rightScroll = 0
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	m.selectedIndex = m.list.Index()
	return m, cmd
}

func (m model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Help modal overlay
	if m.showHelp {
		return m.renderHelpModal()
	}

	// Main layout
	leftPanel := m.renderLeftPanel()
	rightPanel := m.renderRightPanel()
	statusBar := m.renderStatusBar()

	// Create main layout
	main := lipgloss.JoinHorizontal(
		lipgloss.Top,
		leftPanel,
		rightPanel,
	)

	// Add status bar at bottom
	return lipgloss.JoinVertical(
		lipgloss.Left,
		main,
		statusBar,
	)
}

// buildRightLines constructs the lines that will be shown in the right panel
// for a given record, wrapped to the appropriate width according to current
// model dimensions and currentMode.
func (m model) buildRightLines(rec DRD4Record) []string {
	rightWidth := (m.width * 2) / 3
	wrapWidth := rightWidth - 12 // account for padding and borders
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	// sequence content depending on mode
	var seqText string
	switch m.currentMode {
	case modeNucleotides:
		seqText = rec.Nucleotides
	case modeTranslated:
		seqText = rec.Translated
	case modeAlignment:
		seqText = rec.NucleotidesAlign
	}
	// Normalize and split into wrapped lines — return only sequence lines (no header/meta)
	seqText = strings.ReplaceAll(seqText, "\r", "")
	lines := []string{}
	for _, ln := range strings.Split(seqText, "\n") {
		if ln == "" {
			lines = append(lines, "")
			continue
		}
		for len(ln) > wrapWidth {
			lines = append(lines, ln[:wrapWidth])
			ln = ln[wrapWidth:]
		}
		lines = append(lines, ln)
	}

	return lines
}

func (m model) renderLeftPanel() string {
	listWidth := m.width / 3

	// Style the list container
	listContainer := containerStyle.
		Width(listWidth - 2). // Account for padding
		Height(m.height - 4). // Account for status bar
		Render(m.list.View())

	return listContainer
}

func (m model) renderRightPanel() string {
	rightWidth := (m.width * 2) / 3

	if len(m.records) == 0 {
		return containerStyle.
			Width(rightWidth - 2).
			Height(m.height - 4).
			Render("No records available")
	}

	selectedItem := m.list.SelectedItem()
	if selectedItem == nil {
		return containerStyle.
			Width(rightWidth - 2).
			Height(m.height - 4).
			Render("No item selected")
	}

	record := selectedItem.(listItem).record

	// Header with variant info
	header := titleStyle.Render(fmt.Sprintf("%s - %s (%s)", record.VariantCode, record.Name, func() string {
		if record.TranslationSource != "" {
			return record.TranslationSource
		}
		return "unknown"
	}()))

	// Metadata line: source, PB and AA counts
	src := record.TranslationSource
	if src == "" {
		src = "unknown"
	}
	// colorize the source token and make PB/AA use the same color/style
	var srcStyle lipgloss.Style
	switch record.TranslationSource {
	case "ncbi":
		srcStyle = sourceNCBIStyle
	case "seqkit":
		srcStyle = sourceSeqkitStyle
	default:
		srcStyle = sourceUnknownStyle
	}

	// Build meta parts: label (muted) and colored tokens for source/PB/AA
	label := lipgloss.NewStyle().Foreground(mutedColor)
	srcColored := srcStyle.Render(record.TranslationSource)
	pbColored := srcStyle.Render(fmt.Sprintf("PB: %d", record.PBCount))
	aaColored := srcStyle.Render(fmt.Sprintf("AA: %d", record.AACount))

	metaStr := label.Render("Source: ") + srcColored + label.Render("    ") + pbColored + label.Render("    ") + aaColored

	// Content based on current mode. If right panel is focused we render
	// the prepared rightContentLines and honor rightScroll (visible window).
	var content string
	if m.rightFocused {
		// build lines dynamically from selected record
		lines := m.buildRightLines(record)
		if len(lines) > 0 {
			// Determine visible sequence rows for the right panel. Header/meta occupy fixed lines.
			totalVisible := m.height - 6
			if totalVisible < 5 {
				totalVisible = 5
			}
			headerLines := 4
			visible := totalVisible - headerLines
			if visible < 1 {
				visible = 1
			}

			// Clamp scroll
			if m.rightScroll < 0 {
				m.rightScroll = 0
			}

			start := m.rightScroll
			end := start + visible
			if start > len(lines) {
				start = len(lines)
			}
			if end > len(lines) {
				end = len(lines)
			}

			visibleSlice := strings.Join(lines[start:end], "\n")

			// Build header with focus indicator and mode title so the title never disappears
			// no focus indicator text (we keep UI minimal)
			focusIndicator := ""

			headerLine := fmt.Sprintf("%s - %s (%s)%s", record.VariantCode, record.Name, m.currentMode.String(), focusIndicator)
			headerRendered := titleStyle.Render(headerLine)

			// Render the visible sequence slice
			seqRendered := sequenceStyle.
				Width(rightWidth - 6).
				Render(visibleSlice)

			// Compose header, meta and sequence into the focused panel and return
			panelContentFocused := lipgloss.JoinVertical(
				lipgloss.Left,
				headerRendered,
				metaStr,
				"",
				seqRendered,
			)

			return containerStyle.
				Width(rightWidth - 2).
				Height(m.height - 4).
				Render(panelContentFocused)
		}
	} else {
		switch m.currentMode {
		case modeNucleotides:
			content = m.formatSequence(record.Nucleotides, "Nucleotides")
		case modeTranslated:
			content = m.formatSequence(record.Translated, "Translated Sequence")
		case modeAlignment:
			content = m.formatSequence(record.NucleotidesAlign, "Nucleotides Alignment")
		}
	}

	// Combine header and content
	panelContent := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		metaStr,
		"",
		content,
	)

	return containerStyle.
		Width(rightWidth - 2).
		Height(m.height - 4).
		Render(panelContent)
}

func (m model) formatSequence(sequence, title string) string {
	if sequence == "" {
		return lipgloss.NewStyle().
			Foreground(mutedColor).
			Render(fmt.Sprintf("No %s available", strings.ToLower(title)))
	}

	// Remove line breaks and format for display
	cleanSequence := strings.ReplaceAll(sequence, "\n", "")
	cleanSequence = strings.ReplaceAll(cleanSequence, "\r", "")

	// Add title
	titleStr := lipgloss.NewStyle().
		Foreground(accentColor).
		Bold(true).
		Render(title + ":")

	// Format sequence with wrapping
	sequenceContent := sequenceStyle.
		Width(m.width*2/3 - 6). // Account for padding and borders
		Render(cleanSequence)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		titleStr,
		"",
		sequenceContent,
	)
}

func (m model) renderStatusBar() string {
	// Left side - navigation info
	leftInfo := fmt.Sprintf("📊 %d/%d variants", m.selectedIndex+1, m.totalRecords)

	// Center - current mode
	centerInfo := fmt.Sprintf("Mode: %s", m.currentMode.String())

	// Right side - help hint
	rightInfo := "Press 'h' for help • 'q' to quit"

	// Calculate spacing
	totalUsed := len(leftInfo) + len(centerInfo) + len(rightInfo)
	spacing := m.width - totalUsed - 6 // Account for padding

	var statusContent string
	if spacing > 0 {
		leftSpacing := spacing / 2
		rightSpacing := spacing - leftSpacing

		statusContent = fmt.Sprintf("%s%s%s%s%s",
			leftInfo,
			strings.Repeat(" ", leftSpacing),
			centerInfo,
			strings.Repeat(" ", rightSpacing),
			rightInfo,
		)
	} else {
		// Fallback for narrow terminals
		statusContent = fmt.Sprintf("%s | %s", leftInfo, centerInfo)
	}

	return statusBarStyle.
		Width(m.width).
		Render(statusContent)
}

func (m model) renderHelpModal() string {
	helpContent := `🧬 DRD4 Variants Browser - Help

Navigation:
  ↑/↓, j/k     Navigate list
  /            Filter variants
  Enter        Select variant

View Modes:
  1            Show nucleotides
  2            Show translated sequence  
  3            Show nucleotides alignment

General:
  h            Toggle this help
  q, Ctrl+C    Quit application

Current Mode: ` + m.currentMode.String() + `
Total Variants: ` + fmt.Sprintf("%d", m.totalRecords) + `
`

	// Create modal box
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(primaryColor).
		Padding(1, 2).
		Background(surfaceColor).
		Foreground(textColor).
		Width(60).
		Align(lipgloss.Center)

	modal := modalStyle.Render(helpContent)

	// Center the modal on screen
	return lipgloss.Place(
		m.width,
		m.height,
		lipgloss.Center,
		lipgloss.Center,
		modal,
	)
}

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error: %v", err)
		os.Exit(1)
	}
}
