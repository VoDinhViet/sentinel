package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"
)

const (
	dashboardRefresh = 5 * time.Second
	minColumn        = 6
)

func (a *app) tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal dashboard: backups, uptime checks and open alerts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if info, err := os.Stdout.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
				return fmt.Errorf("tui needs a terminal; use `sentinel status --output json` in scripts")
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			st, err := a.openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			ctx := cmd.Context()
			load := func() (statusReport, error) { return buildStatusReport(ctx, cfg, st, time.Now()) }
			_, err = tea.NewProgram(newDashboard(load), tea.WithContext(ctx)).Run()
			return err
		},
	}
}

// Severity of one dashboard line, shown as a colour.
type severity int

const (
	sevOK severity = iota
	sevWarn
	sevBad
)

// dashLine is one table line of a dashboard tab. detail is the full text shown
// under the table for the selected line, since cells are shortened to fit.
type dashLine struct {
	cells    []string
	severity severity
	detail   string
}

type dashTab struct {
	title   string
	headers []string
	lines   []dashLine
	// statusCol is the column coloured by severity.
	statusCol int
}

type dashboard struct {
	load    func() (statusReport, error)
	report  statusReport
	loadErr error
	loaded  bool

	tab      int
	cursor   int
	width    int
	height   int
	quitting bool
}

type reportMsg struct {
	report statusReport
	err    error
}

type refreshMsg struct{}

func newDashboard(load func() (statusReport, error)) dashboard {
	return dashboard{load: load, width: 100, height: 30}
}

func (d dashboard) Init() tea.Cmd {
	return tea.Batch(d.reload(), d.scheduleRefresh())
}

func (d dashboard) reload() tea.Cmd {
	return func() tea.Msg {
		report, err := d.load()
		return reportMsg{report: report, err: err}
	}
}

func (d dashboard) scheduleRefresh() tea.Cmd {
	return tea.Tick(dashboardRefresh, func(time.Time) tea.Msg { return refreshMsg{} })
}

func (d dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
	case reportMsg:
		d.loaded, d.loadErr = true, msg.err
		if msg.err == nil {
			d.report = msg.report
		}
		d.cursor = min(d.cursor, max(len(d.tabs()[d.tab].lines)-1, 0))
	case refreshMsg:
		return d, tea.Batch(d.reload(), d.scheduleRefresh())
	case tea.KeyPressMsg:
		return d.onKey(msg.String())
	}
	return d, nil
}

func (d dashboard) onKey(key string) (tea.Model, tea.Cmd) {
	tabCount := len(d.tabs())
	switch key {
	case "q", "ctrl+c", "esc":
		d.quitting = true
		return d, tea.Quit
	case "tab", "right", "l":
		d.tab, d.cursor = (d.tab+1)%tabCount, 0
	case "shift+tab", "left", "h":
		d.tab, d.cursor = (d.tab+tabCount-1)%tabCount, 0
	case "1", "2", "3":
		d.tab, d.cursor = int(key[0]-'1'), 0
	case "down", "j":
		d.cursor = min(d.cursor+1, max(len(d.tabs()[d.tab].lines)-1, 0))
	case "up", "k":
		d.cursor = max(d.cursor-1, 0)
	case "g", "home":
		d.cursor = 0
	case "G", "end":
		d.cursor = max(len(d.tabs()[d.tab].lines)-1, 0)
	case "r":
		return d, d.reload()
	}
	return d, nil
}

// tabs turns the report into the three dashboard tabs.
func (d dashboard) tabs() []dashTab {
	now := d.report.GeneratedAt
	backups := dashTab{title: "Backups", headers: []string{"JOB", "REPOSITORY", "LAST RUN", "RESULT", "SNAPSHOT", "ADDED"}, statusCol: 3}
	for _, backup := range d.report.Backups {
		line := dashLine{
			cells:  []string{backup.Job, backup.Repository, ago(backup.FinishedAt, now), "ok", shortID(backup.SnapshotID), humanBytes(backup.BytesAdded)},
			detail: backup.Job + " → " + backup.Repository + ": " + backup.Result,
		}
		switch {
		case !backup.Success:
			line.severity, line.cells[3], line.cells[4], line.cells[5] = sevBad, "FAILED", "", ""
		case backup.Warning != "":
			line.severity, line.cells[3] = sevWarn, "warning"
		}
		backups.lines = append(backups.lines, line)
	}
	checks := dashTab{title: "Uptime", headers: []string{"CHECK", "STATUS", "LATENCY", "LAST CHECK"}, statusCol: 1}
	for _, check := range d.report.Checks {
		line := dashLine{
			cells:  []string{check.Name, "UP", (time.Duration(check.LatencyMs) * time.Millisecond).String(), ago(check.At, now)},
			detail: check.Name + ": up",
		}
		if !check.OK {
			line.severity, line.cells[1], line.detail = sevBad, "DOWN", check.Name+": "+check.Error
		}
		checks.lines = append(checks.lines, line)
	}
	alerts := dashTab{title: "Alerts", headers: []string{"ALERT", "SINCE", "MESSAGE"}, statusCol: -1}
	for _, alert := range d.report.Alerts {
		alerts.lines = append(alerts.lines, dashLine{
			cells:    []string{alert.Key, ago(alert.Since, now), alert.Message},
			severity: sevBad,
			detail:   alert.Key + ": " + alert.Message,
		})
	}
	return []dashTab{backups, checks, alerts}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

var (
	colourOK   = lipgloss.Color("2")
	colourWarn = lipgloss.Color("3")
	colourBad  = lipgloss.Color("1")
	colourAcc  = lipgloss.Color("4")

	titleStyle    = lipgloss.NewStyle().Bold(true)
	dimStyle      = lipgloss.NewStyle().Faint(true)
	headerStyle   = lipgloss.NewStyle().Bold(true).Faint(true)
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	activeTab     = lipgloss.NewStyle().Bold(true).Foreground(colourAcc)
)

func severityStyle(s severity) lipgloss.Style {
	switch s {
	case sevBad:
		return lipgloss.NewStyle().Foreground(colourBad).Bold(true)
	case sevWarn:
		return lipgloss.NewStyle().Foreground(colourWarn).Bold(true)
	}
	return lipgloss.NewStyle().Foreground(colourOK)
}

func (d dashboard) View() tea.View {
	view := tea.NewView(d.render())
	view.AltScreen = true
	return view
}

func (d dashboard) render() string {
	if d.quitting {
		return ""
	}
	tabs := d.tabs()
	var out []string

	updated := "loading…"
	if d.loaded {
		updated = "updated " + d.report.GeneratedAt.Local().Format("15:04:05")
	}
	out = append(out, titleStyle.Render("Sentinel")+dimStyle.Render("  "+d.report.Server+"  ·  "+updated), "")

	var tabBar []string
	for i, tab := range tabs {
		label := fmt.Sprintf("%d %s", i+1, tab.title)
		if n := len(tab.lines); n > 0 {
			label += fmt.Sprintf(" (%d)", n)
		}
		switch {
		case i == d.tab:
			tabBar = append(tabBar, activeTab.Render(label))
		case i == 2 && len(tab.lines) > 0:
			tabBar = append(tabBar, severityStyle(sevBad).Render(label))
		default:
			tabBar = append(tabBar, dimStyle.Render(label))
		}
	}
	out = append(out, strings.Join(tabBar, "   "), "")

	if d.loadErr != nil {
		out = append(out, severityStyle(sevBad).Render("could not read the store: "+d.loadErr.Error()), "")
	}

	tab := tabs[d.tab]
	// Lines available for the table: the screen minus header, tabs, column
	// header, detail box and help line.
	visible := max(d.height-10, 3)
	out = append(out, d.renderTable(tab, visible)...)

	out = append(out, "")
	if len(tab.lines) > 0 {
		out = append(out, wrap(tab.lines[d.cursor].detail, d.width-2))
	} else {
		out = append(out, dimStyle.Render(emptyText(d.tab)))
	}
	out = append(out, "", dimStyle.Render("tab switch · ↑↓ select · r refresh · q quit"))
	return strings.Join(out, "\n")
}

func emptyText(tab int) string {
	return [...]string{
		"No backups recorded yet. Run `sentinel backup run <job>` or start the daemon.",
		"No uptime checks recorded yet.",
		"No open alerts.",
	}[tab]
}

func (d dashboard) renderTable(tab dashTab, visible int) []string {
	if len(tab.lines) == 0 {
		return nil
	}
	widths := make([]int, len(tab.headers))
	for i, header := range tab.headers {
		widths[i] = lipgloss.Width(header)
	}
	for _, line := range tab.lines {
		for i, cell := range line.cells {
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
	}
	const gap = 2
	// Columns are ordered by importance: on a narrow screen the last ones are dropped.
	keep := len(widths)
	for keep > 2 && 2+gap*(keep-1)+keep*minColumn > d.width {
		keep--
	}
	widths = widths[:keep]
	fitWidths(widths, d.width-2, gap)

	cellsLine := func(cells []string, style func(col int, text string) string) string {
		cells = cells[:min(len(cells), keep)]
		parts := make([]string, len(cells))
		for i, cell := range cells {
			text := truncate(cell, widths[i])
			text += strings.Repeat(" ", max(widths[i]-lipgloss.Width(text), 0))
			parts[i] = style(i, text)
		}
		return strings.Join(parts, strings.Repeat(" ", gap))
	}

	out := []string{"  " + cellsLine(tab.headers, func(_ int, text string) string { return headerStyle.Render(text) })}
	first := 0
	if d.cursor >= visible {
		first = d.cursor - visible + 1
	}
	for i := first; i < min(first+visible, len(tab.lines)); i++ {
		line := tab.lines[i]
		rendered := cellsLine(line.cells, func(col int, text string) string {
			if col == tab.statusCol {
				return severityStyle(line.severity).Render(text)
			}
			return text
		})
		if i == d.cursor {
			// Reverse video on the plain text keeps the selection readable on any theme.
			plain := cellsLine(line.cells, func(_ int, text string) string { return text })
			out = append(out, selectedStyle.Render("› "+plain))
			continue
		}
		out = append(out, "  "+rendered)
	}
	if hidden := len(tab.lines) - (first + visible); hidden > 0 {
		out = append(out, dimStyle.Render(fmt.Sprintf("  … %d more", hidden)))
	}
	return out
}

// fitWidths shrinks the widest columns until the table fits in total cells.
func fitWidths(widths []int, total, gap int) {
	const minimum = minColumn
	for {
		used := gap * (len(widths) - 1)
		widest := 0
		for i, w := range widths {
			used += w
			if w > widths[widest] {
				widest = i
			}
		}
		excess := used - total
		if excess <= 0 || widths[widest] <= minimum {
			return
		}
		widths[widest] -= min(excess, widths[widest]-minimum)
	}
}

func truncate(text string, width int) string {
	if lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// wrap breaks text into lines of at most width cells, on spaces.
func wrap(text string, width int) string {
	if width < 20 {
		width = 20
	}
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		if current != "" && lipgloss.Width(current)+1+lipgloss.Width(word) > width {
			lines = append(lines, current)
			current = word
			continue
		}
		current = strings.TrimSpace(current + " " + word)
	}
	return strings.Join(append(lines, current), "\n")
}
