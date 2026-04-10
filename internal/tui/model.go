package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// StepStatus tracks the state of an individual step.
type StepStatus int

const (
	StepPending StepStatus = iota
	StepActive
	StepDone
	StepFailed
)

// Step holds the display info for one phase.
type Step struct {
	Name     string
	Status   StepStatus
	Detail   string  // short detail shown next to step name
	Indent   int     // 0 = top-level, 1 = sub-step (indented)
	Progress float64 // 0.0–1.0; rendered as bar when > 0 and StepActive
}

// DoneConfig controls what is shown when all work completes successfully.
type DoneConfig struct {
	SuccessMsg string   // e.g. `Instance "dev" is ready!`
	HintLines  []string // e.g. ["SSH: abox ssh dev"]
}

// PhaseStartMsg marks a phase as active.
type PhaseStartMsg struct{ Phase int }

// PhaseDoneMsg marks a phase as complete (or failed).
type PhaseDoneMsg struct {
	Phase int
	Err   error
}

// PhaseProgressMsg updates the progress bar for an active phase.
type PhaseProgressMsg struct {
	Phase   int
	Percent float64 // 0.0–1.0
	Detail  string  // e.g. "230 / 512 MB"
}

// ProgressMsg carries a line of output from the work goroutine.
type ProgressMsg struct {
	Line string
}

// WarnMsg carries a warning line from the work goroutine (stderr/slog).
type WarnMsg struct {
	Line string
}

// allDoneMsg signals that all work is complete.
type allDoneMsg struct{ err error }

// tickMsg drives the spinner animation.
type tickMsg struct{}

// StepModel is the bubbletea model for a step-checklist TUI.
type StepModel struct {
	label       string
	done        DoneConfig
	steps       []Step
	logLines    []string // ring buffer of output lines
	warnLines   []string // ring buffer of warning lines
	logExpanded bool     // toggle: compact (10 lines) vs expanded (40 lines)
	finished    bool
	err         error
	tick        int // spinner frame counter
	width       int // terminal width
}

const (
	maxLogLines      = 1000 // ring buffer capacity
	maxWarnLines     = 100  // ring buffer capacity for warnings
	compactLogLines  = 10
	expandedLogLines = 40
	spinnerInterval  = 100 // ms
)

// braille spinner frames
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Styles
var (
	headerStyle     = lipgloss.NewStyle().Bold(true)
	doneStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	failStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	activeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	pendingStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	dimStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	borderStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	warnBorderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

// NewStepModel creates a new StepModel.
func NewStepModel(label string, steps []Step, done DoneConfig) StepModel {
	return StepModel{
		label:    label,
		done:     done,
		steps:    steps,
		logLines: make([]string, 0, 128),
	}
}

// Err returns the error captured by the model (if any).
func (m StepModel) Err() error {
	return m.err
}

func (m StepModel) Init() tea.Cmd {
	return tickCmd()
}

func (m StepModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKeyPress(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tickMsg:
		return m.handleTick()
	case PhaseStartMsg:
		m.handlePhaseStart(msg)
		return m, nil
	case PhaseDoneMsg:
		m.handlePhaseDone(msg)
		return m, nil
	case PhaseProgressMsg:
		m.handlePhaseProgress(msg)
		return m, nil
	case ProgressMsg:
		m.handleProgress(msg)
		return m, nil
	case WarnMsg:
		m.handleWarn(msg)
		return m, nil
	case allDoneMsg:
		m.finished = true
		m.err = msg.err
		return m, tea.Quit
	}
	return m, nil
}

func (m StepModel) handleKeyPress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.finished = true
		m.err = fmt.Errorf("interrupted")
		return m, tea.Quit
	case "v":
		m.logExpanded = !m.logExpanded
		return m, nil
	}
	return m, nil
}

func (m StepModel) handleTick() (tea.Model, tea.Cmd) {
	if m.finished {
		return m, nil
	}
	m.tick++
	return m, tickCmd()
}

func (m *StepModel) handlePhaseStart(msg PhaseStartMsg) {
	if msg.Phase >= 0 && msg.Phase < len(m.steps) {
		// Only activate if still pending (idempotent).
		if m.steps[msg.Phase].Status == StepPending {
			m.steps[msg.Phase].Status = StepActive
		}
	}
}

func (m *StepModel) handlePhaseDone(msg PhaseDoneMsg) {
	if msg.Phase >= 0 && msg.Phase < len(m.steps) {
		if msg.Err != nil {
			m.steps[msg.Phase].Status = StepFailed
			m.steps[msg.Phase].Detail = msg.Err.Error()
		} else {
			m.steps[msg.Phase].Status = StepDone
			m.steps[msg.Phase].Progress = 0 // clear bar on completion
		}
	}
}

func (m *StepModel) handlePhaseProgress(msg PhaseProgressMsg) {
	if msg.Phase >= 0 && msg.Phase < len(m.steps) {
		m.steps[msg.Phase].Progress = msg.Percent
		m.steps[msg.Phase].Detail = msg.Detail
	}
}

func (m *StepModel) handleProgress(msg ProgressMsg) {
	// Append to log ring buffer. The re-slice doesn't shrink the
	// backing array, but with maxLogLines=1000 and short-lived CLI
	// sessions the memory overhead is negligible.
	m.logLines = append(m.logLines, msg.Line)
	if len(m.logLines) > maxLogLines {
		excess := len(m.logLines) - maxLogLines
		m.logLines = m.logLines[excess:]
	}
}

func (m *StepModel) handleWarn(msg WarnMsg) {
	m.warnLines = append(m.warnLines, msg.Line)
	if len(m.warnLines) > maxWarnLines {
		excess := len(m.warnLines) - maxWarnLines
		m.warnLines = m.warnLines[excess:]
	}
}

func (m StepModel) View() tea.View {
	var sb strings.Builder

	w := m.width
	if w <= 0 {
		w = 80
	}

	// Header
	sb.WriteString(headerStyle.Render(m.label))
	sb.WriteString("\n\n")

	m.renderSteps(&sb, w)

	// Log tail area (show when there are log lines, but hide on success)
	if len(m.logLines) > 0 && (!m.finished || m.err != nil) {
		m.renderLogPanel(&sb, w)
	}

	// Warnings panel (always visible when there are warnings, even on success)
	if len(m.warnLines) > 0 {
		m.renderWarnPanel(&sb, w)
	}

	// Final message
	if m.finished {
		m.renderFinalMessage(&sb)
	}

	return tea.NewView(sb.String())
}

func (m StepModel) renderSteps(sb *strings.Builder, w int) {
	for _, s := range m.steps {
		// Indentation: 2 spaces per indent level + base 2
		indent := strings.Repeat("  ", s.Indent+1)
		sb.WriteString(indent)

		switch s.Status {
		case StepDone:
			sb.WriteString(doneStyle.Render("✓"))
		case StepFailed:
			sb.WriteString(failStyle.Render("✗"))
		case StepActive:
			frame := spinnerFrames[m.tick%len(spinnerFrames)]
			sb.WriteString(activeStyle.Render(frame))
		default: // pending
			sb.WriteString(pendingStyle.Render("○"))
		}
		sb.WriteString(" ")
		sb.WriteString(s.Name)
		showProgressBar := s.Status == StepActive && s.Progress > 0
		if s.Detail != "" && !showProgressBar {
			sb.WriteString(dimStyle.Render(fmt.Sprintf(" (%s)", s.Detail)))
		}
		sb.WriteString("\n")

		// Progress bar below active step
		if showProgressBar {
			sb.WriteString(renderProgressBar(w, s.Progress, s.Detail))
			sb.WriteString("\n")
		}
	}
}

func (m StepModel) renderLogPanel(sb *strings.Builder, w int) {
	sb.WriteString("\n")

	// Build header line with toggle hint
	hint := "[v] expand"
	if m.logExpanded {
		hint = "[v] collapse"
	}
	lineWidth := max(w-4, 20)

	label := " output "
	hintStr := " " + hint + " "
	dashCount := max(lineWidth-len(label)-len(hintStr), 2)
	leftDashes := 3
	rightDashes := max(dashCount-leftDashes, 1)

	headerLine := "  " + borderStyle.Render(strings.Repeat("─", leftDashes)+label) +
		borderStyle.Render(strings.Repeat("─", rightDashes)+hintStr)
	sb.WriteString(headerLine)
	sb.WriteString("\n")

	// Determine how many lines to show
	visibleLines := compactLogLines
	if m.logExpanded {
		visibleLines = expandedLogLines
	}

	// Get the tail of log lines
	start := 0
	if len(m.logLines) > visibleLines {
		start = len(m.logLines) - visibleLines
	}
	tail := m.logLines[start:]

	for _, line := range tail {
		// Truncate long lines to terminal width
		display := line
		if len(display) > lineWidth {
			display = display[:lineWidth-1] + "…"
		}
		sb.WriteString("  ")
		sb.WriteString(display)
		sb.WriteString("\n")
	}

	footerLine := "  " + borderStyle.Render(strings.Repeat("─", lineWidth))
	sb.WriteString(footerLine)
	sb.WriteString("\n")
}

func (m StepModel) renderWarnPanel(sb *strings.Builder, w int) {
	sb.WriteString("\n")

	lineWidth := max(w-4, 20)

	warnLabel := " warnings "
	warnDashCount := max(lineWidth-len(warnLabel), 2)
	warnLeftDashes := 3
	warnRightDashes := max(warnDashCount-warnLeftDashes, 1)

	warnHeaderLine := "  " + warnBorderStyle.Render(strings.Repeat("─", warnLeftDashes)+warnLabel) +
		warnBorderStyle.Render(strings.Repeat("─", warnRightDashes))
	sb.WriteString(warnHeaderLine)
	sb.WriteString("\n")

	for _, line := range m.warnLines {
		display := line
		if len(display) > lineWidth {
			display = display[:lineWidth-1] + "…"
		}
		sb.WriteString("  ")
		sb.WriteString(warnBorderStyle.Render(display))
		sb.WriteString("\n")
	}

	warnFooterLine := "  " + warnBorderStyle.Render(strings.Repeat("─", lineWidth))
	sb.WriteString(warnFooterLine)
	sb.WriteString("\n")
}

func (m StepModel) renderFinalMessage(sb *strings.Builder) {
	if m.err == nil {
		sb.WriteString("\n")
		sb.WriteString(doneStyle.Render(m.done.SuccessMsg))
		sb.WriteString("\n")
		for _, line := range m.done.HintLines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("\n")
		sb.WriteString(failStyle.Render(fmt.Sprintf("Error: %v", m.err)))
		sb.WriteString("\n")
	}
}

// renderProgressBar renders a lipgloss progress bar line.
func renderProgressBar(width int, pct float64, detail string) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 1 {
		pct = 1
	}

	// "    ████░░░░ 45.2%  230 / 512 MB"
	pctStr := fmt.Sprintf("%4.1f%%", pct*100)
	detailStr := ""
	if detail != "" {
		detailStr = "  " + detail
	}

	// Reserve space: 4 indent + 1 space + pctStr + detailStr + 2 margin
	reserved := 4 + 1 + len(pctStr) + len(detailStr) + 2
	barWidth := max(width-reserved, 10)

	filled := min(int(pct*float64(barWidth)), barWidth)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	return fmt.Sprintf("    %s %s%s", activeStyle.Render(bar), pctStr, detailStr)
}

func tickCmd() tea.Cmd {
	return tea.Tick(
		time.Duration(spinnerInterval)*time.Millisecond,
		func(_ time.Time) tea.Msg {
			return tickMsg{}
		},
	)
}
