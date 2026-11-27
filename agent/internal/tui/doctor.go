package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"sentinel/agent/internal/doctor"
)

// doctorTab is the index of the Doctor tab.
const doctorTab = 3

// doctorMsg carries the result of the checks started by startDoctor.
type doctorMsg struct{ findings []doctor.Finding }

// doctorState is what the Doctor tab shows: nothing yet, a run in progress, or the findings.
type doctorState struct {
	started  bool // the checks were started at least once; they run when the tab is first opened
	running  bool
	findings []doctor.Finding
}

// startDoctorIfNeeded starts the checks when the Doctor tab is on screen and has not run
// yet. Update calls it after every message, so every way into the tab runs them.
func (m manageModel) startDoctorIfNeeded(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if !m.onTabs() || m.tab != doctorTab || m.doctor.started {
		return m, cmd
	}
	next, start := m.startDoctor()
	return next, tea.Batch(cmd, start)
}

// startDoctor runs the checks in the background; it is also what r does on the Doctor tab.
func (m manageModel) startDoctor() (manageModel, tea.Cmd) {
	if m.actions.doctor == nil || m.doctor.running {
		return m, nil
	}
	m.doctor.started, m.doctor.running = true, true
	check, ctx := m.actions.doctor, m.ctx
	return m, func() tea.Msg { return doctorMsg{findings: check(ctx)} }
}

// doctorSeverity maps a finding to the colour of its line.
func doctorSeverity(finding doctor.Finding) severity {
	switch {
	case finding.Err == nil:
		return sevOK
	case finding.Warn:
		return sevWarn
	}
	return sevBad
}

// doctorProblems counts the findings that are failures; doctorWarnings those that only warn.
func (m manageModel) doctorProblems() int { return m.doctorFindings(sevBad) }

func (m manageModel) doctorWarnings() int { return m.doctorFindings(sevWarn) }

func (m manageModel) doctorFindings(level severity) int {
	found := 0
	for _, finding := range m.doctor.findings {
		if doctorSeverity(finding) == level {
			found++
		}
	}
	return found
}

func (m manageModel) buildDoctorTab() manageTab {
	tab := manageTab{title: "Doctor", headers: []string{"KIỂM TRA", "TRẠNG THÁI", "CHI TIẾT"}, statusCol: 1, historyCol: -1, alarm: m.doctorProblems() > 0, count: m.doctorProblems() + m.doctorWarnings()}
	for _, finding := range m.doctor.findings {
		level := doctorSeverity(finding)
		status, message := "ok", finding.Info
		switch level {
		case sevBad:
			status, message = "LỖI", finding.Err.Error()
		case sevWarn:
			status, message = "cảnh báo", finding.Err.Error()
		}
		detail := finding.Name + ": " + message
		if finding.Hint != "" && level != sevOK {
			detail += "  → " + finding.Hint
		}
		tab.lines = append(tab.lines, manageLine{
			cells:    []string{finding.Name, status, strings.TrimSpace(message)},
			severity: level,
			detail:   detail,
		})
	}
	return tab
}

func (m manageModel) doctorWaitText() string {
	if m.doctor.running {
		return "Đang kiểm tra công cụ, file trạng thái và các repository… (repository không phản hồi có thể mất một lúc)"
	}
	return "Bấm r để chạy kiểm tra."
}
