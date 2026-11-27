package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/agent/internal/status"
)

func newManageModel(ctx context.Context, load func() (status.Report, error), actions manageActions, jobRepositories []jobRepository) manageModel {
	return manageModel{ctx: ctx, load: load, actions: actions, jobRepositories: jobRepositories, width: 100, height: 30, hits: &hitMap{}}
}

func (m manageModel) Init() tea.Cmd {
	return tea.Batch(m.reloadPeriodic(), tea.RequestBackgroundColor)
}

// reload loads the report once, after an action changed something.
func (m manageModel) reload() tea.Cmd { return m.loadReport(false) }

// reloadPeriodic is the timed refresh: the next one is scheduled when its report arrives.
func (m manageModel) reloadPeriodic() tea.Cmd { return m.loadReport(true) }

func (m manageModel) loadReport(periodic bool) tea.Cmd {
	asked := time.Now()
	return func() tea.Msg {
		report, err := m.load()
		return reportMsg{report: report, err: err, asked: asked, periodic: periodic}
	}
}

func (m manageModel) scheduleRefresh() tea.Cmd {
	every := manageRefresh
	if len(m.report.Running) > 0 {
		every = manageRefreshRunning // watch a backup's progress closely
	}
	return tea.Tick(every, func(time.Time) tea.Msg { return refreshMsg{} })
}

func (m manageModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.update(msg)
	next := model.(manageModel)
	if next.onTabs() && !next.config.loaded { // the schedules in the Backups tab and the Config tab come from the file
		next = next.refreshConfig()
	}
	next, cmd = next.startServerIfNeeded(cmd)
	return next.startDoctorIfNeeded(cmd)
}

func (m manageModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		applyPalette(msg.IsDark()) // the terminal answered: switch to the palette that suits its background
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = max(msg.Width-frameCols, 1), max(msg.Height-frameRows, 1)
	case reportMsg:
		if !msg.asked.Before(m.reportAsked) { // an older load answering after a newer one must not undo it
			m.reportAsked = msg.asked
			m.loaded, m.loadErr = true, msg.err
			if msg.err == nil {
				m.report = msg.report
			}
			m.cursor = min(m.cursor, max(len(m.tabs()[m.tab].lines)-1, 0))
		}
		if msg.periodic {
			return m, m.scheduleRefresh()
		}
	case folderSizedMsg:
		return m.onFolderSized(msg), nil
	case deepSearchDueMsg:
		return m, m.onDeepSearchDue(msg)
	case deepSearchedMsg:
		return m.onDeepSearched(msg)
	case doctorMsg:
		m.doctor.running, m.doctor.findings = false, msg.findings
		m.cursor = min(m.cursor, max(len(m.tabs()[m.tab].lines)-1, 0))
	case hostReadMsg:
		m.server.reading, m.server.report = false, msg.report
		m.cursor = min(m.cursor, max(len(m.tabs()[m.tab].lines)-1, 0))
	case refreshMsg:
		next, readHost := m.refreshServer()
		return next, tea.Batch(m.reloadPeriodic(), readHost)
	case spinMsg:
		return m.onSpin()
	case backupStartedMsg:
		return m.afterBackupStarted(msg)
	case backupStopRequestedMsg:
		return m.afterBackupStopRequested(msg)
	case snapshotsMsg:
		return m.afterSnapshots(msg), nil
	case restoreDoneMsg:
		return m.afterRestore(msg), m.reload()
	case notifierTestedMsg:
		return m.afterNotifierTest(msg), nil
	case connectTestedMsg:
		return m.afterConnect(msg), nil
	case tea.KeyPressMsg:
		return m.onKey(msg.String(), msg.Text)
	case tea.PasteMsg:
		return m.onPaste(msg.Content), nil
	case tea.MouseClickMsg:
		return m.onClick(msg.X, msg.Y, msg.Button == tea.MouseLeft)
	case tea.MouseWheelMsg:
		if _, ok := m.top().(browseScreen); ok {
			return m.browseWheel(msg.Button == tea.MouseWheelUp, msg.Button == tea.MouseWheelDown)
		}
		return m.onWheel(msg.Button == tea.MouseWheelUp, msg.Button == tea.MouseWheelDown), nil
	}
	return m, nil
}

func (m manageModel) onKey(key, text string) (tea.Model, tea.Cmd) {
	if open := m.top(); open != nil {
		next, cmd := open.onKey(m, key, text)
		return next, cmd
	}
	return m.onTableKey(key)
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
