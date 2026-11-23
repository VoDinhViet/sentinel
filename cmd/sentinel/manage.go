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
			model := newManageModel(ctx, load, newManageActions(cfg, st, a.configPath), configuredJobRepositories(cfg))
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
	// history is the outcome of the latest runs, oldest first (Backups tab only).
	history []string
}

type manageTab struct {
	title   string
	headers []string
	lines   []manageLine
	// statusCol is the column coloured by severity; historyCol (-1 = none) shows line.history.
	statusCol, historyCol int
	// alarm colours the tab label red.
	alarm bool
}

type manageMode int

const (
	modeTable     manageMode = iota // the three tabs
	modeSnapshots                   // snapshots of the selected job and repository
	modeTarget                      // typing the directory to restore into
	modeConfirm                     // typing the job name to confirm a restore
	modeBusy                        // a backup, snapshot listing or restore is running
	modeResult                      // the outcome of a restore
	modeForm                        // adding or editing a database job
	modeDelete                      // typing the job name to confirm its removal
	modeHelp                        // the key list
)

type manageModel struct {
	ctx             context.Context
	load            func() (statusReport, error)
	actions         manageActions
	jobRepositories []jobRepository
	report          statusReport
	loadErr         error
	loaded          bool

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

	form       jobForm
	deleteName string

	helpReturn manageMode // the screen the key list was opened from

	doctor doctorState
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

func newManageModel(ctx context.Context, load func() (statusReport, error), actions manageActions, jobRepositories []jobRepository) manageModel {
	return manageModel{ctx: ctx, load: load, actions: actions, jobRepositories: jobRepositories, width: 100, height: 30}
}

func (m manageModel) Init() tea.Cmd {
	return tea.Batch(m.reload(), m.scheduleRefresh())
}

func (m manageModel) reload() tea.Cmd {
	return func() tea.Msg {
		report, err := m.load()
		return reportMsg{report: report, err: err}
	}
}

func (m manageModel) scheduleRefresh() tea.Cmd {
	return tea.Tick(manageRefresh, func(time.Time) tea.Msg { return refreshMsg{} })
}

func (m manageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.update(msg)
	return model.(manageModel).startDoctorIfNeeded(cmd)
}

func (m manageModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case reportMsg:
		m.loaded, m.loadErr = true, msg.err
		if msg.err == nil {
			m.report = msg.report
		}
		m.cursor = min(m.cursor, max(len(m.tabs()[m.tab].lines)-1, 0))
	case doctorMsg:
		m.doctor.running, m.doctor.findings = false, msg.findings
		m.cursor = min(m.cursor, max(len(m.tabs()[m.tab].lines)-1, 0))
	case refreshMsg:
		return m, tea.Batch(m.reload(), m.scheduleRefresh())
	case spinMsg:
		if m.mode == modeBusy {
			m.busyFrame++
			return m, m.spin()
		}
	case backupDoneMsg:
		return m.afterBackup(msg)
	case snapshotsMsg:
		return m.afterSnapshots(msg), nil
	case restoreDoneMsg:
		return m.afterRestore(msg), m.reload()
	case tea.KeyPressMsg:
		return m.onKey(msg.String(), msg.Text)
	case tea.MouseClickMsg:
		return m.onClick(msg.X, msg.Y, msg.Button == tea.MouseLeft)
	case tea.MouseWheelMsg:
		return m.onWheel(msg.Button == tea.MouseWheelUp, msg.Button == tea.MouseWheelDown), nil
	}
	return m, nil
}

func (m manageModel) spin() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

// startBusy switches to the busy screen and returns the context the action runs
// with; ctrl+c on the busy screen cancels it.
func (m *manageModel) startBusy(text string) context.Context {
	ctx, cancel := context.WithCancel(m.ctx)
	m.mode, m.busyText, m.busyStart, m.busyFrame, m.cancelBusy = modeBusy, text, time.Now(), 0, cancel
	m.notice = ""
	return ctx
}

func (m *manageModel) endBusy() {
	if m.cancelBusy != nil {
		m.cancelBusy()
	}
	m.cancelBusy = nil
}

func (m manageModel) onKey(key, text string) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeBusy:
		if key == "ctrl+c" && m.cancelBusy != nil {
			m.cancelBusy()
			m.busyText += " (cancelling: restic may take up to 30s to stop cleanly)"
		}
		return m, nil
	case modeResult:
		m.mode = modeTable
		if m.snapshotJob != "" && !m.resultBad {
			m.mode = modeSnapshots
		}
		return m, nil
	case modeSnapshots:
		return m.onSnapshotsKey(key)
	case modeTarget:
		return m.onTargetKey(key, text)
	case modeConfirm:
		return m.onConfirmKey(key, text)
	case modeForm:
		return m.onFormKey(key, text)
	case modeDelete:
		return m.onDeleteKey(key, text)
	case modeHelp:
		m.mode = m.helpReturn
		return m, nil
	}
	return m.onTableKey(key)
}

func (m manageModel) onTableKey(key string) (tea.Model, tea.Cmd) {
	tabCount := len(m.tabs())
	m.notice = ""
	switch key {
	case "?":
		m.helpReturn, m.mode = modeTable, modeHelp
	case "q", "ctrl+c", "esc":
		m.quitting = true
		return m, tea.Quit
	case "tab", "right", "l":
		m.tab, m.cursor = (m.tab+1)%tabCount, 0
	case "shift+tab", "left", "h":
		m.tab, m.cursor = (m.tab+tabCount-1)%tabCount, 0
	case "1", "2", "3", "4":
		m.tab, m.cursor = int(key[0]-'1'), 0
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.tabs()[m.tab].lines)-1, 0))
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(len(m.tabs()[m.tab].lines)-1, 0)
	case "r":
		if m.tab == doctorTab {
			next, start := m.startDoctor()
			return next, start
		}
		return m, m.reload()
	case "b":
		if line, ok := m.selectedBackup(); ok {
			ctx := m.startBusy(fmt.Sprintf("Backing up %s → %s", line.job, line.repository))
			job, repository, actions := line.job, line.repository, m.actions
			return m, tea.Batch(m.spin(), func() tea.Msg {
				runs, err := actions.runBackup(ctx, job, repository)
				return backupDoneMsg{job: job, repository: repository, runs: runs, err: err}
			})
		}
	case "a":
		if m.tab == 0 {
			return m.startAdd(), nil
		}
	case "e":
		if m.tab == 0 {
			return m.startEdit(), nil
		}
	case "d":
		if m.tab == 0 {
			return m.startDelete(), nil
		}
	case "s", "enter":
		if line, ok := m.selectedBackup(); ok {
			ctx := m.startBusy(fmt.Sprintf("Listing snapshots of %s in %s", line.job, line.repository))
			job, repository, actions := line.job, line.repository, m.actions
			return m, tea.Batch(m.spin(), func() tea.Msg {
				snapshots, err := actions.snapshots(ctx, job, repository)
				return snapshotsMsg{job: job, repository: repository, snapshots: snapshots, err: err}
			})
		}
	}
	return m, nil
}

// selectedBackup is the backup line under the cursor, on the Backups tab.
func (m manageModel) selectedBackup() (manageLine, bool) {
	lines := m.tabs()[m.tab].lines
	if m.tab != 0 || m.cursor >= len(lines) {
		return manageLine{}, false
	}
	return lines[m.cursor], true
}

func (m manageModel) afterBackup(msg backupDoneMsg) (tea.Model, tea.Cmd) {
	m.endBusy()
	m.mode = modeTable
	switch {
	case msg.err != nil:
		m.notice, m.noticeBad = fmt.Sprintf("%s → %s: %v", msg.job, msg.repository, msg.err), true
	case len(msg.runs) == 0:
		m.notice, m.noticeBad = fmt.Sprintf("%s → %s: no run was reported", msg.job, msg.repository), true
	default:
		run := msg.runs[0]
		m.notice, m.noticeBad = fmt.Sprintf("%s → %s: %s", msg.job, msg.repository, runResult(run)), !run.Success
		if run.Success {
			m.notice += "  " + shortID(run.SnapshotID)
		}
	}
	return m, m.reload()
}

func (m manageModel) afterSnapshots(msg snapshotsMsg) manageModel {
	m.endBusy()
	if msg.err != nil {
		m.mode, m.notice, m.noticeBad = modeTable, fmt.Sprintf("%s → %s: %v", msg.job, msg.repository, msg.err), true
		return m
	}
	m.mode, m.snapshotJob, m.snapshotRepo, m.snapshots, m.snapshotCursor = modeSnapshots, msg.job, msg.repository, msg.snapshots, 0
	return m
}

func (m manageModel) onSnapshotsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "?":
		m.helpReturn, m.mode = modeSnapshots, modeHelp
	case "esc", "q":
		m.mode = modeTable
	case "down", "j":
		m.snapshotCursor = min(m.snapshotCursor+1, max(len(m.snapshots)-1, 0))
	case "up", "k":
		m.snapshotCursor = max(m.snapshotCursor-1, 0)
	case "g", "home":
		m.snapshotCursor = 0
	case "G", "end":
		m.snapshotCursor = max(len(m.snapshots)-1, 0)
	case "enter", "r":
		if len(m.snapshots) > 0 {
			m.chosen, m.mode, m.target, m.inputError = m.snapshots[m.snapshotCursor], modeTarget, "", ""
		}
	}
	return m, nil
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

func (m manageModel) onTargetKey(key, text string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = modeSnapshots
	case "enter":
		target := strings.TrimSpace(m.target)
		if !strings.HasPrefix(target, "/") {
			m.inputError = "enter an absolute path, e.g. /tmp/restore"
			return m, nil
		}
		m.target, m.mode, m.confirm, m.inputError = target, modeConfirm, "", ""
	default:
		m.target, m.inputError = editText(m.target, key, text), ""
	}
	return m, nil
}

func (m manageModel) onConfirmKey(key, text string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = modeSnapshots
	case "enter":
		if m.confirm != m.snapshotJob {
			m.inputError = "the name does not match"
			return m, nil
		}
		ctx := m.startBusy(fmt.Sprintf("Restoring %s of %s into %s", shortID(m.chosen.ID), m.snapshotJob, m.target))
		job, repository, snapshot, target, actions := m.snapshotJob, m.snapshotRepo, m.chosen, m.target, m.actions
		return m, tea.Batch(m.spin(), func() tea.Msg {
			hint, err := actions.restore(ctx, job, repository, snapshot, target)
			return restoreDoneMsg{hint: hint, err: err}
		})
	default:
		m.confirm, m.inputError = editText(m.confirm, key, text), ""
	}
	return m, nil
}

func (m manageModel) afterRestore(msg restoreDoneMsg) manageModel {
	m.endBusy()
	m.mode, m.resultBad = modeResult, msg.err != nil
	if msg.err != nil {
		m.resultText = "Restore failed: " + msg.err.Error()
		return m
	}
	m.resultText = fmt.Sprintf("Restored %s of %s from %s into %s.", shortID(m.chosen.ID), m.snapshotJob, m.snapshotRepo, m.target)
	if msg.hint != "" {
		m.resultText += "\n\nLoad it with:\n  " + msg.hint + "\n\nThis replaces existing data: try it on a scratch database or server first."
	}
	return m
}

// tabs turns the report into the three manager tabs.
func (m manageModel) tabs() []manageTab {
	now := m.report.GeneratedAt
	// Columns are ordered by importance: a narrow screen drops the last ones.
	backups := manageTab{title: "Backups", headers: []string{"JOB", "REPOSITORY", "LAST RUN", "RESULT", "HISTORY", "SNAPSHOT", "ADDED"}, statusCol: 3, historyCol: 4}
	for _, backup := range m.report.Backups {
		line := manageLine{
			cells:   []string{backup.Job, backup.Repository, relativeAgo(backup.FinishedAt, now), "ok", "", shortID(backup.SnapshotID), humanBytes(backup.BytesAdded)},
			detail:  backup.Job + " → " + backup.Repository + ": " + backup.Result,
			job:     backup.Job,
			history: backup.Recent, repository: backup.Repository,
		}
		switch {
		case !backup.Success:
			line.severity, line.cells[3], line.cells[5], line.cells[6] = sevBad, "FAILED", "", ""
		case backup.Warning != "":
			line.severity, line.cells[3] = sevWarn, "warning"
		}
		backups.lines = append(backups.lines, line)
	}
	for _, jobRepo := range m.jobRepositories {
		if !m.hasBackupLine(jobRepo) {
			backups.lines = append(backups.lines, manageLine{
				cells:    []string{jobRepo.job, jobRepo.repository, "never", "never run", "", "", ""},
				severity: sevWarn,
				detail:   jobRepo.job + " → " + jobRepo.repository + ": never run. Press b to back it up now.",
				job:      jobRepo.job, repository: jobRepo.repository,
			})
		}
	}
	checks := manageTab{title: "Uptime", headers: []string{"CHECK", "STATUS", "LATENCY", "LAST CHECK"}, statusCol: 1, historyCol: -1}
	for _, check := range m.report.Checks {
		line := manageLine{
			cells:  []string{check.Name, "UP", (time.Duration(check.LatencyMs) * time.Millisecond).String(), relativeAgo(check.At, now)},
			detail: check.Name + ": up",
		}
		if !check.OK {
			line.severity, line.cells[1], line.detail = sevBad, "DOWN", check.Name+": "+check.Error
		}
		checks.lines = append(checks.lines, line)
	}
	alerts := manageTab{title: "Alerts", headers: []string{"ALERT", "SINCE", "MESSAGE"}, statusCol: -1, historyCol: -1}
	for _, alert := range m.report.Alerts {
		alerts.lines = append(alerts.lines, manageLine{
			cells:    []string{alert.Key, relativeAgo(alert.Since, now), alert.Message},
			severity: sevBad,
			detail:   alert.Key + ": " + alert.Message,
		})
	}
	return []manageTab{backups, checks, alerts, m.buildDoctorTab()}
}

// summary is the one line under the title: what needs attention, or "all good".
func (m manageModel) summary() string {
	var healthy, warning, failed, neverRun int
	for _, line := range m.tabs()[0].lines {
		switch {
		case line.cells[3] == "never run":
			neverRun++
		case line.severity == sevBad:
			failed++
		case line.severity == sevWarn:
			warning++
		default:
			healthy++
		}
	}
	checksDown := 0
	for _, check := range m.report.Checks {
		if !check.OK {
			checksDown++
		}
	}
	type part struct {
		text  string
		level severity
	}
	var parts []part
	add := func(count int, label string, level severity) {
		if count > 0 {
			parts = append(parts, part{fmt.Sprintf("%d %s", count, label), level})
		}
	}
	add(failed, "failed", sevBad)
	add(len(m.report.Alerts), plural(len(m.report.Alerts), "alert", "alerts"), sevBad)
	add(checksDown, plural(checksDown, "check down", "checks down"), sevBad)
	add(warning, plural(warning, "warning", "warnings"), sevWarn)
	add(neverRun, "never run", sevWarn)
	add(healthy, "healthy", sevOK)
	if len(parts) == 0 {
		return dimStyle.Render("nothing configured yet")
	}
	// On a narrow screen the least important parts (the last) are dropped.
	const separator = " · "
	for len(parts) > 1 {
		total := len(separator) * (len(parts) - 1)
		for _, p := range parts {
			total += lipgloss.Width(p.text)
		}
		if total <= m.width {
			break
		}
		parts = parts[:len(parts)-1]
	}
	rendered := make([]string, len(parts))
	for i, p := range parts {
		rendered[i] = severityStyle(p.level).Render(p.text)
	}
	return strings.Join(rendered, dimStyle.Render(separator))
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

func (m manageModel) hasBackupLine(jobRepo jobRepository) bool {
	for _, backup := range m.report.Backups {
		if backup.Job == jobRepo.job && backup.Repository == jobRepo.repository {
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
	keyStyle            = lipgloss.NewStyle().Bold(true).Foreground(colourAcc)
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

func (m manageModel) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m manageModel) render() string {
	if m.quitting {
		return ""
	}
	screenLines := m.renderHeader()
	switch m.mode {
	case modeBusy:
		screenLines = append(screenLines, m.renderBusy()...)
	case modeSnapshots:
		screenLines = append(screenLines, m.renderSnapshots()...)
	case modeTarget:
		screenLines = append(screenLines, m.renderTarget()...)
	case modeConfirm:
		screenLines = append(screenLines, m.renderConfirm()...)
	case modeResult:
		screenLines = append(screenLines, m.renderResult()...)
	case modeForm:
		screenLines = append(screenLines, m.renderForm()...)
	case modeDelete:
		screenLines = append(screenLines, m.renderDelete()...)
	case modeHelp:
		screenLines = append(screenLines, m.renderHelp()...)
	default:
		screenLines = append(screenLines, m.renderTabs()...)
	}
	return strings.Join(screenLines, "\n")
}

func (m manageModel) renderHeader() []string {
	updated := "loading…"
	if m.loaded {
		updated = "updated " + m.report.GeneratedAt.Local().Format("15:04:05")
	}
	title := lipglossStyleAccent.Render("◆ ") + titleStyle.Render("Sentinel") + dimStyle.Render("  "+m.report.Server+"  ·  "+updated)
	if m.mode != modeTable || !m.loaded {
		return []string{title, ""}
	}
	return []string{title, m.summary(), ""}
}

func (m manageModel) renderTabs() []string {
	tabs := m.tabs()
	var screenLines []string
	var tabBar []string
	for i, label := range m.tabLabels() {
		switch {
		case i == m.tab:
			tabBar = append(tabBar, activeTab.Render(label))
		case (i == 2 && len(tabs[i].lines) > 0) || tabs[i].alarm:
			tabBar = append(tabBar, severityStyle(sevBad).Render(label))
		default:
			tabBar = append(tabBar, dimStyle.Render(label))
		}
	}
	rule := dimStyle.Render(strings.Repeat("─", m.width))
	screenLines = append(screenLines, strings.Join(tabBar, m.tabGap()), rule)

	if m.loadErr != nil {
		screenLines = append(screenLines, severityStyle(sevBad).Render("could not read the store: "+m.loadErr.Error()), "")
	}

	tab := tabs[m.tab]
	// Lines available for the table: the screen minus title, summary, tabs, rules,
	// column header, detail, notice and footer.
	screenLines = append(screenLines, m.renderTable(tab, m.visibleRows())...)

	screenLines = append(screenLines, rule)
	if len(tab.lines) > 0 {
		screenLines = append(screenLines, wrap(tab.lines[m.cursor].detail, m.width-2))
	} else {
		screenLines = append(screenLines, dimStyle.Render(m.emptyText(m.tab)))
	}
	if m.notice != "" {
		style := severityStyle(sevOK)
		if m.noticeBad {
			style = severityStyle(sevBad)
		}
		screenLines = append(screenLines, style.Render(wrap(m.notice, m.width-2)))
	}
	footer := m.keyBar("tab", "switch", "↑↓", "select", "r", "refresh", "?", "help", "q", "quit")
	if m.tab == doctorTab {
		footer = m.keyBar("tab", "switch", "↑↓", "select", "r", "run checks", "?", "help", "q", "quit")
	}
	if m.tab == 0 {
		footer = m.keyBar("b", "backup", "s", "snapshots", "a", "add", "e", "edit", "d", "remove", "?", "help", "q", "quit")
	}
	return append(screenLines, "", footer)
}

// tabLabels are the tab bar's labels, e.g. "1 Backups 3"; the tab bar joins them with tabGap().
func (m manageModel) tabLabels() []string {
	var labels []string
	for i, tab := range m.tabs() {
		label := fmt.Sprintf("%d %s", i+1, tab.title)
		if n := len(tab.lines); n > 0 {
			label += fmt.Sprintf(" %d", n)
		}
		labels = append(labels, label)
	}
	return labels
}

// tabGap separates the tab labels; a narrow screen packs them closer.
func (m manageModel) tabGap() string {
	if m.width < 60 {
		return " "
	}
	return "   "
}

// visibleRows is how many table lines fit: the screen minus title, summary, tabs,
// rules, column header, detail, notice and footer.
func (m manageModel) visibleRows() int { return max(m.height-13, 3) }

func (m manageModel) emptyText(tab int) string {
	if tab == doctorTab {
		return m.doctorWaitText()
	}
	return [...]string{
		"No backups recorded yet. Run `sentinel backup run <job>` or start the daemon.",
		"No uptime checks recorded yet.",
		"No open alerts.",
	}[tab]
}

func (m manageModel) renderTable(tab manageTab, visible int) []string {
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
		if tab.historyCol >= 0 {
			widths[tab.historyCol] = max(widths[tab.historyCol], len(line.history))
		}
	}
	const gap = 2
	const prefix = 4 // the selection marker and the status dot
	// Columns are ordered by importance: on a narrow screen the last ones are dropped.
	keep := len(widths)
	for keep > 2 && prefix+gap*(keep-1)+keep*minColumn > m.width {
		keep--
	}
	widths = widths[:keep]
	fitWidths(widths, m.width-prefix, gap)

	// cell renders one cell padded to its column; plainOnly leaves out colours (for the selected line).
	cell := func(col int, line manageLine, plainOnly bool) string {
		if col == tab.historyCol {
			glyphs := line.history[max(len(line.history)-widths[col], 0):]
			var text strings.Builder
			for _, outcome := range glyphs {
				glyph, level := outcomeGlyph(outcome)
				if plainOnly {
					text.WriteString(glyph)
				} else {
					text.WriteString(severityStyle(level).Render(glyph))
				}
			}
			return text.String() + strings.Repeat(" ", widths[col]-len(glyphs))
		}
		text := truncate(line.cells[col], widths[col])
		text += strings.Repeat(" ", max(widths[col]-lipgloss.Width(text), 0))
		if col == tab.statusCol && !plainOnly {
			return severityStyle(line.severity).Render(text)
		}
		return text
	}
	joined := func(line manageLine, plainOnly bool) string {
		parts := make([]string, keep)
		for col := range parts {
			parts[col] = cell(col, line, plainOnly)
		}
		return strings.Join(parts, strings.Repeat(" ", gap))
	}

	header := make([]string, keep)
	for col := range header {
		text := truncate(tab.headers[col], widths[col])
		header[col] = headerStyle.Render(text + strings.Repeat(" ", max(widths[col]-lipgloss.Width(text), 0)))
	}
	screenLines := []string{strings.Repeat(" ", prefix) + strings.Join(header, strings.Repeat(" ", gap))}
	first := 0
	if m.cursor >= visible {
		first = m.cursor - visible + 1
	}
	for i := first; i < min(first+visible, len(tab.lines)); i++ {
		line := tab.lines[i]
		if i == m.cursor {
			// Reverse video on plain text keeps the selection readable on any terminal theme.
			screenLines = append(screenLines, selectedStyle.Render("› ● "+joined(line, true)))
			continue
		}
		screenLines = append(screenLines, "  "+severityStyle(line.severity).Render("●")+" "+joined(line, false))
	}
	if hidden := len(tab.lines) - (first + visible); hidden > 0 {
		screenLines = append(screenLines, dimStyle.Render(fmt.Sprintf("    … %d more", hidden)))
	}
	return screenLines
}

// outcomeGlyph is the one-cell symbol and colour of a run outcome in the history column.
func outcomeGlyph(outcome string) (string, severity) {
	switch outcome {
	case "failed":
		return "✗", sevBad
	case "warning":
		return "▲", sevWarn
	}
	return "✓", sevOK
}

// keyBar renders key hints ("b", "backup", "s", "snapshots", ...) as a footer line;
// when the screen is too narrow the labels are dropped and only the keys remain.
func (m manageModel) keyBar(pairs ...string) string {
	var full, keysOnly []string
	for i := 0; i+1 < len(pairs); i += 2 {
		key, label := pairs[i], pairs[i+1]
		full = append(full, keyStyle.Render(key)+" "+dimStyle.Render(label))
		keysOnly = append(keysOnly, keyStyle.Render(key))
	}
	bar := strings.Join(full, "  ")
	if lipgloss.Width(bar) > m.width {
		bar = strings.Join(keysOnly, " ")
	}
	return bar
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
