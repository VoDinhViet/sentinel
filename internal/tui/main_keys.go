package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// The keys of the tabs, the screen under all the others.

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
	case "1", "2", "3", "4", "5", "6", "7":
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
		switch m.tab {
		case doctorTab:
			next, start := m.startDoctor()
			return next, start
		case serverTab:
			next, read := m.startServer()
			return next, read
		case fleetTab:
			next, ask := m.startFleet()
			return next, ask
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
