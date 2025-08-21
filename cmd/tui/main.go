package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
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

	selectedStyle = lipgloss.NewStyle().
			Foreground(primaryColor).
			Bold(true)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(textColor).
			Background(surfaceColor).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().
			Foreground(mutedColor).
			Italic(true)

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
}

func initialModel() model {
	// Load data
	data, err := ioutil.ReadFile("database.json")
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

	case tea.KeyMsg:
		switch msg.String() {
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

	// Content based on current mode
	var content string
	switch m.currentMode {
	case modeNucleotides:
		content = m.formatSequence(record.Nucleotides, "Nucleotides")
	case modeTranslated:
		content = m.formatSequence(record.Translated, "Translated Sequence")
	case modeAlignment:
		content = m.formatSequence(record.NucleotidesAlign, "Nucleotides Alignment")
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
