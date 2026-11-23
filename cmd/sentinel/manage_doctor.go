package main

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// doctorTab is the index of the Doctor tab.
const doctorTab = 3

// doctorMsg carries the result of the checks started by startDoctor.
type doctorMsg struct{ findings []finding }

// doctorState is what the Doctor tab shows: nothing yet, a run in progress, or the findings.
type doctorState struct {
	started  bool // the checks were started at least once; they run when the tab is first opened
	running  bool
	findings []finding
}

// startDoctorIfNeeded starts the checks when the Doctor tab is on screen and has not run
// yet. Update calls it after every message, so every way into the tab runs them.
func (m manageModel) startDoctorIfNeeded(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if m.mode != modeTable || m.tab != doctorTab || m.doctor.started {
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
func doctorSeverity(f finding) severity {
	switch {
	case f.err == nil:
		return sevOK
	case f.warn:
		return sevWarn
	}
	return sevBad
}

// doctorProblems counts the findings that are failures.
func (m manageModel) doctorProblems() int {
	failed := 0
	for _, f := range m.doctor.findings {
		if doctorSeverity(f) == sevBad {
			failed++
		}
	}
	return failed
}

func (m manageModel) buildDoctorTab() manageTab {
	tab := manageTab{title: "Doctor", headers: []string{"CHECK", "STATUS", "DETAIL"}, statusCol: 1, historyCol: -1, alarm: m.doctorProblems() > 0}
	for _, f := range m.doctor.findings {
		level := doctorSeverity(f)
		status, message := "ok", f.info
		switch level {
		case sevBad:
			status, message = "FAIL", f.err.Error()
		case sevWarn:
			status, message = "warn", f.err.Error()
		}
		detail := f.name + ": " + message
		if f.hint != "" && level != sevOK {
			detail += "  → " + f.hint
		}
		tab.lines = append(tab.lines, manageLine{
			cells:    []string{f.name, status, strings.TrimSpace(message)},
			severity: level,
			detail:   detail,
		})
	}
	return tab
}

func (m manageModel) doctorWaitText() string {
	if m.doctor.running {
		return "Checking tools, the store and the repositories… (a repository that does not answer can take a while)"
	}
	return "Press r to run the checks."
}
