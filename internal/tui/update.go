package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/status"
)

func newManageModel(ctx context.Context, load func() (status.Report, error), actions manageActions, jobRepositories []jobRepository) manageModel {
	return manageModel{ctx: ctx, load: load, actions: actions, jobRepositories: jobRepositories, width: 100, height: 30, hits: &hitMap{}}
}

func (m manageModel) Init() tea.Cmd {
	return tea.Batch(m.reload(), m.scheduleRefresh(), tea.RequestBackgroundColor)
}

func (m manageModel) reload() tea.Cmd {
	return func() tea.Msg {
		report, err := m.load()
		return reportMsg{report: report, err: err}
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
		m.loaded, m.loadErr = true, msg.err
		if msg.err == nil {
			m.report = msg.report
		}
		m.cursor = min(m.cursor, max(len(m.tabs()[m.tab].lines)-1, 0))
	case folderSizedMsg:
		return m.onFolderSized(msg), nil
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
	case tea.MouseClickMsg:
		return m.onClick(msg.X, msg.Y, msg.Button == tea.MouseLeft)
	case tea.MouseWheelMsg:
		if m.mode == modeBrowse {
			return m.browseWheel(msg.Button == tea.MouseWheelUp, msg.Button == tea.MouseWheelDown)
		}
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

// onTabs says the tabs are on show: no screen is open over them.
func (m manageModel) onTabs() bool { return len(m.screens) == 0 && m.mode == modeTable }

func (m manageModel) onKey(key, text string) (tea.Model, tea.Cmd) {
	if open := m.top(); open != nil {
		next, cmd := open.onKey(m, key, text)
		return next, cmd
	}
	switch m.mode {
	case modeBusy:
		if key == "ctrl+c" && m.cancelBusy != nil {
			m.cancelBusy()
			m.busyText += " (đang huỷ: restic có thể mất tới 30 giây để dừng gọn)"
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
	case modeBrowse:
		return m.onBrowseKey(key, text)
	}
	return m.onTableKey(key)
}

func (m manageModel) onTableKey(key string) (tea.Model, tea.Cmd) {
	tabCount := len(m.tabs())
	m.notice = ""
	if key != "x" {
		m.stopAsked = ""
	}
	switch key {
	case "?":
		m = m.push(m.tabHelp())
	case "q", "ctrl+c", "esc":
		m.quitting = true
		return m, tea.Quit
	case "tab", "right", "l":
		m.tab, m.cursor = (m.tab+1)%tabCount, 0
	case "shift+tab", "left", "h":
		m.tab, m.cursor = (m.tab+tabCount-1)%tabCount, 0
	case "1", "2", "3", "4", "5":
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
			if line.running {
				m.notice = fmt.Sprintf("%s → %s đang chạy rồi (bấm x để dừng)", line.job, line.repository)
				return m, nil
			}
			job, repository, actions := line.job, line.repository, m.actions
			m.notice = fmt.Sprintf("Đang khởi động backup %s → %s…", job, repository)
			return m, func() tea.Msg {
				pid, logPath, err := actions.startBackup(job, repository)
				return backupStartedMsg{job: job, repository: repository, pid: pid, logPath: logPath, err: err}
			}
		}
	case "x":
		if line, ok := m.selectedBackup(); ok && line.running {
			if m.stopAsked != line.job {
				m.stopAsked = line.job
				m.notice, m.noticeBad = fmt.Sprintf("Dừng backup %s? Bấm x lần nữa để xác nhận (lần backup sau sẽ phải chạy lại từ đầu)", line.job), true
				return m, nil
			}
			m.stopAsked = ""
			job, actions := line.job, m.actions
			return m, func() tea.Msg { return backupStopRequestedMsg{job: job, err: actions.stopBackup(job)} }
		}
	case "t":
		if m.tab == configTab {
			return m.configTestNotifier()
		}
	case "a":
		if m.tab == configTab {
			return m.configAdd(), nil
		}
		if m.tab == backupsTab {
			return m.startAdd(), nil
		}
	case "e":
		if m.tab == configTab {
			return m.configEdit(), nil
		}
		if m.tab == backupsTab {
			return m.startEdit(), nil
		}
	case "d":
		if m.tab == configTab {
			return m.configRemove(), nil
		}
		if m.tab == backupsTab {
			return m.startDelete(), nil
		}
	case "s", "enter":
		if m.tab == configTab {
			return m.configEdit(), nil
		}
		if line, ok := m.selectedBackup(); ok {
			ctx := m.startBusy(fmt.Sprintf("Đang liệt kê snapshot của %s trong %s", line.job, line.repository))
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
	if m.tab != backupsTab || m.cursor >= len(lines) {
		return manageLine{}, false
	}
	return lines[m.cursor], true
}

func (m manageModel) afterBackupStarted(msg backupStartedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notice, m.noticeBad = fmt.Sprintf("%s → %s: %v", msg.job, msg.repository, msg.err), true
		return m, m.reload()
	}
	m.notice, m.noticeBad = fmt.Sprintf("%s → %s: đã chạy nền (pid %d), đóng manage cũng không sao. Log: %s", msg.job, msg.repository, msg.pid, msg.logPath), false
	return m, m.reload()
}

func (m manageModel) afterBackupStopRequested(msg backupStopRequestedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notice, m.noticeBad = fmt.Sprintf("%s: không dừng được: %v", msg.job, msg.err), true
		return m, nil
	}
	m.notice, m.noticeBad = msg.job+": đã gửi yêu cầu dừng, backup sẽ dừng trong vài giây", false
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
		m = m.push(snapshotsHelp())
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
	if key != "tab" {
		m.targetOptions = nil
	}
	switch key {
	case "tab":
		completed, options := completePath(m.target)
		if completed != m.target || len(options) == 0 {
			m.target = completed
		}
		m.targetOptions = options
	case "esc":
		m.mode = modeSnapshots
	case "enter":
		target := strings.TrimSpace(m.target)
		if !strings.HasPrefix(target, "/") {
			m.inputError = "nhập đường dẫn tuyệt đối, ví dụ /tmp/restore"
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
			m.inputError = "tên không khớp"
			return m, nil
		}
		ctx := m.startBusy(fmt.Sprintf("Đang restore %s của %s vào %s", shortID(m.chosen.ID), m.snapshotJob, m.target))
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
		m.resultText = "Restore thất bại: " + msg.err.Error()
		return m
	}
	m.resultText = fmt.Sprintf("Đã restore %s của %s từ %s vào %s.", shortID(m.chosen.ID), m.snapshotJob, m.snapshotRepo, m.target)
	if msg.hint != "" {
		m.resultText += "\n\nNạp lại bằng lệnh:\n  " + msg.hint + "\n\nLệnh này thay thế dữ liệu hiện có: hãy thử trên database hoặc server tạm trước."
	}
	return m
}
