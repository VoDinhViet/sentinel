package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"sentinel/internal/backup"
	"sentinel/internal/store"
)

const (
	manageRefresh = 5 * time.Second
	minColumn     = 6
)

func (a *app) manageCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "manage",
		Aliases: []string{"tui"},
		Short:   "Interactive terminal manager: backups, snapshots, restore, uptime checks and alerts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if info, err := os.Stdout.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
				return fmt.Errorf("manage needs a terminal; use `sentinel status --output json` in scripts")
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
			model := newManager(ctx, load, newManageActions(cfg, st), configuredPairs(cfg))
			_, err = tea.NewProgram(model, tea.WithContext(ctx)).Run()
			return err
		},
	}
}

// Severity of one manager line, shown as a colour.
type severity int

const (
	sevOK severity = iota
	sevWarn
	sevBad
)

// manageLine is one table line of a manager tab. detail is the full text shown
// under the table for the selected line, since cells are shortened to fit.
type manageLine struct {
	cells    []string
	severity severity
	detail   string
	// job and repository identify the backup a line stands for (Backups tab only).
	job, repository string
}

type manageTab struct {
	title   string
	headers []string
	lines   []manageLine
	// statusCol is the column coloured by severity.
	statusCol int
}

type manageMode int

const (
	modeTable     manageMode = iota // the three tabs
	modeSnapshots                   // snapshots of the selected job and repository
	modeTarget                      // typing the directory to restore into
	modeConfirm                     // typing the job name to confirm a restore
	modeBusy                        // a backup, snapshot listing or restore is running
	modeResult                      // the outcome of a restore
)

type manager struct {
	ctx     context.Context
	load    func() (statusReport, error)
	act     manageActions
	pairs   []jobRepository
	report  statusReport
	loadErr error
	loaded  bool

	mode     manageMode
	tab      int
	cursor   int
	width    int
	height   int
	quitting bool

	// notice is the outcome of the last action, shown under the table.
	notice    string
	noticeBad bool

	snapshotJob, snapshotRepo string
	snapshots                 []backup.Snapshot // newest first
	snapshotCursor            int
	chosen                    backup.Snapshot
	target, confirm           string
	inputError                string

	busyText   string
	busyStart  time.Time
	busyFrame  int
	cancelBusy context.CancelFunc

	resultText string
	resultBad  bool
}

type (
	reportMsg struct {
		report statusReport
		err    error
	}
	refreshMsg    struct{}
	spinMsg       struct{}
	backupDoneMsg struct {
		job, repository string
		runs            []store.BackupRun
		err             error
	}
	snapshotsMsg struct {
		job, repository string
		snapshots       []backup.Snapshot
		err             error
	}
	restoreDoneMsg struct {
		hint string
		err  error
	}
)

func newManager(ctx context.Context, load func() (statusReport, error), act manageActions, pairs []jobRepository) manager {
	return manager{ctx: ctx, load: load, act: act, pairs: pairs, width: 100, height: 30}
}

func (d manager) Init() tea.Cmd {
	return tea.Batch(d.reload(), d.scheduleRefresh())
}

func (d manager) reload() tea.Cmd {
	return func() tea.Msg {
		report, err := d.load()
		return reportMsg{report: report, err: err}
	}
}

func (d manager) scheduleRefresh() tea.Cmd {
	return tea.Tick(manageRefresh, func(time.Time) tea.Msg { return refreshMsg{} })
}

func (d manager) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	case spinMsg:
		if d.mode == modeBusy {
			d.busyFrame++
			return d, d.spin()
		}
	case backupDoneMsg:
		return d.afterBackup(msg)
	case snapshotsMsg:
		return d.afterSnapshots(msg), nil
	case restoreDoneMsg:
		return d.afterRestore(msg), d.reload()
	case tea.KeyPressMsg:
		return d.onKey(msg.String(), msg.Text)
	}
	return d, nil
}

func (d manager) spin() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

// startBusy switches to the busy screen and returns the context the action runs
// with; ctrl+c on the busy screen cancels it.
func (d *manager) startBusy(text string) context.Context {
	ctx, cancel := context.WithCancel(d.ctx)
	d.mode, d.busyText, d.busyStart, d.busyFrame, d.cancelBusy = modeBusy, text, time.Now(), 0, cancel
	d.notice = ""
	return ctx
}

func (d *manager) endBusy() {
	if d.cancelBusy != nil {
		d.cancelBusy()
	}
	d.cancelBusy = nil
}

func (d manager) onKey(key, text string) (tea.Model, tea.Cmd) {
	switch d.mode {
	case modeBusy:
		if key == "ctrl+c" && d.cancelBusy != nil {
			d.cancelBusy()
			d.busyText += " (cancelling: restic may take up to 30s to stop cleanly)"
		}
		return d, nil
	case modeResult:
		d.mode = modeTable
		if d.snapshotJob != "" && !d.resultBad {
			d.mode = modeSnapshots
		}
		return d, nil
	case modeSnapshots:
		return d.onSnapshotsKey(key)
	case modeTarget:
		return d.onTargetKey(key, text)
	case modeConfirm:
		return d.onConfirmKey(key, text)
	}
	return d.onTableKey(key)
}

func (d manager) onTableKey(key string) (tea.Model, tea.Cmd) {
	tabCount := len(d.tabs())
	d.notice = ""
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
	case "b":
		if line, ok := d.selectedBackup(); ok {
			ctx := d.startBusy(fmt.Sprintf("Backing up %s → %s", line.job, line.repository))
			job, repository, act := line.job, line.repository, d.act
			return d, tea.Batch(d.spin(), func() tea.Msg {
				runs, err := act.runBackup(ctx, job, repository)
				return backupDoneMsg{job: job, repository: repository, runs: runs, err: err}
			})
		}
	case "s", "enter":
		if line, ok := d.selectedBackup(); ok {
			ctx := d.startBusy(fmt.Sprintf("Listing snapshots of %s in %s", line.job, line.repository))
			job, repository, act := line.job, line.repository, d.act
			return d, tea.Batch(d.spin(), func() tea.Msg {
				snapshots, err := act.snapshots(ctx, job, repository)
				return snapshotsMsg{job: job, repository: repository, snapshots: snapshots, err: err}
			})
		}
	}
	return d, nil
}

// selectedBackup is the backup line under the cursor, on the Backups tab.
func (d manager) selectedBackup() (manageLine, bool) {
	lines := d.tabs()[d.tab].lines
	if d.tab != 0 || d.cursor >= len(lines) {
		return manageLine{}, false
	}
	return lines[d.cursor], true
}

func (d manager) afterBackup(msg backupDoneMsg) (tea.Model, tea.Cmd) {
	d.endBusy()
	d.mode = modeTable
	switch {
	case msg.err != nil:
		d.notice, d.noticeBad = fmt.Sprintf("%s → %s: %v", msg.job, msg.repository, msg.err), true
	case len(msg.runs) == 0:
		d.notice, d.noticeBad = fmt.Sprintf("%s → %s: no run was reported", msg.job, msg.repository), true
	default:
		run := msg.runs[0]
		d.notice, d.noticeBad = fmt.Sprintf("%s → %s: %s", msg.job, msg.repository, runResult(run)), !run.Success
		if run.Success {
			d.notice += "  " + shortID(run.SnapshotID)
		}
	}
	return d, d.reload()
}

func (d manager) afterSnapshots(msg snapshotsMsg) manager {
	d.endBusy()
	if msg.err != nil {
		d.mode, d.notice, d.noticeBad = modeTable, fmt.Sprintf("%s → %s: %v", msg.job, msg.repository, msg.err), true
		return d
	}
	d.mode, d.snapshotJob, d.snapshotRepo, d.snapshots, d.snapshotCursor = modeSnapshots, msg.job, msg.repository, msg.snapshots, 0
	return d
}

func (d manager) onSnapshotsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "q":
		d.mode = modeTable
	case "down", "j":
		d.snapshotCursor = min(d.snapshotCursor+1, max(len(d.snapshots)-1, 0))
	case "up", "k":
		d.snapshotCursor = max(d.snapshotCursor-1, 0)
	case "g", "home":
		d.snapshotCursor = 0
	case "G", "end":
		d.snapshotCursor = max(len(d.snapshots)-1, 0)
	case "enter", "r":
		if len(d.snapshots) > 0 {
			d.chosen, d.mode, d.target, d.inputError = d.snapshots[d.snapshotCursor], modeTarget, "", ""
		}
	}
	return d, nil
}

// editText applies one key to a single-line text field.
func editText(field, key, text string) string {
	switch {
	case key == "backspace":
		runes := []rune(field)
		return string(runes[:max(len(runes)-1, 0)])
	case text != "" && key != "enter":
		return field + text
	}
	return field
}

func (d manager) onTargetKey(key, text string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		d.mode = modeSnapshots
	case "enter":
		target := strings.TrimSpace(d.target)
		if !strings.HasPrefix(target, "/") {
			d.inputError = "enter an absolute path, e.g. /tmp/restore"
			return d, nil
		}
		d.target, d.mode, d.confirm, d.inputError = target, modeConfirm, "", ""
	default:
		d.target, d.inputError = editText(d.target, key, text), ""
	}
	return d, nil
}

func (d manager) onConfirmKey(key, text string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		d.mode = modeSnapshots
	case "enter":
		if d.confirm != d.snapshotJob {
			d.inputError = "the name does not match"
			return d, nil
		}
		ctx := d.startBusy(fmt.Sprintf("Restoring %s of %s into %s", shortID(d.chosen.ID), d.snapshotJob, d.target))
		job, repository, snapshot, target, act := d.snapshotJob, d.snapshotRepo, d.chosen, d.target, d.act
		return d, tea.Batch(d.spin(), func() tea.Msg {
			hint, err := act.restore(ctx, job, repository, snapshot, target)
			return restoreDoneMsg{hint: hint, err: err}
		})
	default:
		d.confirm, d.inputError = editText(d.confirm, key, text), ""
	}
	return d, nil
}

func (d manager) afterRestore(msg restoreDoneMsg) manager {
	d.endBusy()
	d.mode, d.resultBad = modeResult, msg.err != nil
	if msg.err != nil {
		d.resultText = "Restore failed: " + msg.err.Error()
		return d
	}
	d.resultText = fmt.Sprintf("Restored %s of %s from %s into %s.", shortID(d.chosen.ID), d.snapshotJob, d.snapshotRepo, d.target)
	if msg.hint != "" {
		d.resultText += "\n\nLoad it with:\n  " + msg.hint + "\n\nThis replaces existing data: try it on a scratch database or server first."
	}
	return d
}

// tabs turns the report into the three manager tabs.
func (d manager) tabs() []manageTab {
	now := d.report.GeneratedAt
	backups := manageTab{title: "Backups", headers: []string{"JOB", "REPOSITORY", "LAST RUN", "RESULT", "SNAPSHOT", "ADDED"}, statusCol: 3}
	for _, backup := range d.report.Backups {
		line := manageLine{
			cells:  []string{backup.Job, backup.Repository, ago(backup.FinishedAt, now), "ok", shortID(backup.SnapshotID), humanBytes(backup.BytesAdded)},
			detail: backup.Job + " → " + backup.Repository + ": " + backup.Result,
			job:    backup.Job, repository: backup.Repository,
		}
		switch {
		case !backup.Success:
			line.severity, line.cells[3], line.cells[4], line.cells[5] = sevBad, "FAILED", "", ""
		case backup.Warning != "":
			line.severity, line.cells[3] = sevWarn, "warning"
		}
		backups.lines = append(backups.lines, line)
	}
	for _, pair := range d.pairs {
		if !d.hasBackupLine(pair) {
			backups.lines = append(backups.lines, manageLine{
				cells:    []string{pair.job, pair.repository, "never", "never run", "", ""},
				severity: sevWarn,
				detail:   pair.job + " → " + pair.repository + ": never run. Press b to back it up now.",
				job:      pair.job, repository: pair.repository,
			})
		}
	}
	checks := manageTab{title: "Uptime", headers: []string{"CHECK", "STATUS", "LATENCY", "LAST CHECK"}, statusCol: 1}
	for _, check := range d.report.Checks {
		line := manageLine{
			cells:  []string{check.Name, "UP", (time.Duration(check.LatencyMs) * time.Millisecond).String(), ago(check.At, now)},
			detail: check.Name + ": up",
		}
		if !check.OK {
			line.severity, line.cells[1], line.detail = sevBad, "DOWN", check.Name+": "+check.Error
		}
		checks.lines = append(checks.lines, line)
	}
	alerts := manageTab{title: "Alerts", headers: []string{"ALERT", "SINCE", "MESSAGE"}, statusCol: -1}
	for _, alert := range d.report.Alerts {
		alerts.lines = append(alerts.lines, manageLine{
			cells:    []string{alert.Key, ago(alert.Since, now), alert.Message},
			severity: sevBad,
			detail:   alert.Key + ": " + alert.Message,
		})
	}
	return []manageTab{backups, checks, alerts}
}

func (d manager) hasBackupLine(pair jobRepository) bool {
	for _, backup := range d.report.Backups {
		if backup.Job == pair.job && backup.Repository == pair.repository {
			return true
		}
	}
	return false
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

	titleStyle          = lipgloss.NewStyle().Bold(true)
	dimStyle            = lipgloss.NewStyle().Faint(true)
	headerStyle         = lipgloss.NewStyle().Bold(true).Faint(true)
	selectedStyle       = lipgloss.NewStyle().Reverse(true)
	activeTab           = lipgloss.NewStyle().Bold(true).Foreground(colourAcc)
	lipglossStyleAccent = lipgloss.NewStyle().Foreground(colourAcc)
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

func (d manager) View() tea.View {
	view := tea.NewView(d.render())
	view.AltScreen = true
	return view
}

func (d manager) render() string {
	if d.quitting {
		return ""
	}
	out := d.renderHeader()
	switch d.mode {
	case modeBusy:
		out = append(out, d.renderBusy()...)
	case modeSnapshots:
		out = append(out, d.renderSnapshots()...)
	case modeTarget:
		out = append(out, d.renderTarget()...)
	case modeConfirm:
		out = append(out, d.renderConfirm()...)
	case modeResult:
		out = append(out, d.renderResult()...)
	default:
		out = append(out, d.renderTabs()...)
	}
	return strings.Join(out, "\n")
}

func (d manager) renderHeader() []string {
	updated := "loading…"
	if d.loaded {
		updated = "updated " + d.report.GeneratedAt.Local().Format("15:04:05")
	}
	return []string{titleStyle.Render("Sentinel") + dimStyle.Render("  "+d.report.Server+"  ·  "+updated), ""}
}

func (d manager) renderTabs() []string {
	tabs := d.tabs()
	var out []string
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
	// header, detail box, notice and help line.
	visible := max(d.height-12, 3)
	out = append(out, d.renderTable(tab, visible)...)

	out = append(out, "")
	if len(tab.lines) > 0 {
		out = append(out, wrap(tab.lines[d.cursor].detail, d.width-2))
	} else {
		out = append(out, dimStyle.Render(emptyText(d.tab)))
	}
	if d.notice != "" {
		style := severityStyle(sevOK)
		if d.noticeBad {
			style = severityStyle(sevBad)
		}
		out = append(out, style.Render(wrap(d.notice, d.width-2)))
	}
	help := "tab switch · ↑↓ select · r refresh · q quit"
	if d.tab == 0 {
		help = "b backup now · s snapshots · tab · r · q"
	}
	return append(out, "", dimStyle.Render(help))
}

func emptyText(tab int) string {
	return [...]string{
		"No backups recorded yet. Run `sentinel backup run <job>` or start the daemon.",
		"No uptime checks recorded yet.",
		"No open alerts.",
	}[tab]
}

func (d manager) renderTable(tab manageTab, visible int) []string {
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
