package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func doctorFindings() []finding {
	return []finding{
		{name: "config", info: "/etc/sentinel/config.yaml"},
		{name: "restic", err: errors.New("not found in PATH"), hint: "install restic >= 0.17"},
		{name: "notifiers", err: errors.New("none configured"), warn: true, hint: "add a notifier"},
	}
}

// doctorModel is a loaded screen whose doctor action counts its runs.
func doctorModel(t *testing.T) (manageModel, *int) {
	t.Helper()
	runs := new(int)
	actions := manageActions{doctor: func(context.Context) []finding {
		*runs++
		return doctorFindings()
	}}
	model, _ := newManageModel(context.Background(), func() (statusReport, error) { return sampleReport(), nil }, actions, nil).Update(reportMsg{report: sampleReport()})
	return model.(manageModel), runs
}

// send delivers a key through Update, as the program does, and runs the command it returns
// when that command is the doctor run.
func send(t *testing.T, screen manageModel, key string) (manageModel, tea.Cmd) {
	t.Helper()
	model, cmd := screen.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	return model.(manageModel), cmd
}

func TestDoctorTabRunsTheChecksOnFirstVisitOnly(t *testing.T) {
	screen, runs := doctorModel(t)
	if *runs != 0 {
		t.Fatal("the checks must not run before the tab is opened")
	}
	screen, cmd := send(t, screen, "4")
	if cmd == nil || !screen.doctor.running {
		t.Fatal("opening the tab must start the checks")
	}
	if view := plain(screen.render()); !strings.Contains(view, "Checking") {
		t.Errorf("no progress text while the checks run:\n%s", view)
	}
	done := cmd().(doctorMsg)
	if *runs != 1 || len(done.findings) != 3 {
		t.Fatalf("runs = %d, findings = %d", *runs, len(done.findings))
	}
	model, _ := screen.Update(done)
	screen = model.(manageModel)
	if screen.doctor.running {
		t.Error("still running after the result arrived")
	}

	screen, _ = send(t, screen, "1")
	screen, cmd = send(t, screen, "4")
	if cmd != nil || *runs != 1 {
		t.Error("coming back to the tab must show the last result, not run again")
	}
}

func TestDoctorTabShowsFindingsAndHints(t *testing.T) {
	screen, _ := doctorModel(t)
	model, _ := screen.Update(doctorMsg{findings: doctorFindings()})
	screen = model.(manageModel)
	screen.tab = doctorTab
	view := plain(screen.render())
	for _, want := range []string{"4 Doctor 3", "config", "ok", "FAIL", "warn", "not found in PATH", "none configured"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	screen.cursor = 1
	if view := plain(screen.render()); !strings.Contains(view, "install restic >= 0.17") {
		t.Errorf("the selected failure shows its hint:\n%s", view)
	}
	screen.cursor = 0
	if view := plain(screen.render()); strings.Contains(view, "install restic") {
		t.Error("the hint belongs to the selected line only")
	}
}

func TestDoctorRunsAgainOnRButNotWhileRunning(t *testing.T) {
	screen, runs := doctorModel(t)
	screen, cmd := send(t, screen, "4")
	if _, again := send(t, screen, "r"); again != nil {
		t.Error("r while the checks run must not start a second run")
	}
	model, _ := screen.Update(cmd().(doctorMsg))
	screen = model.(manageModel)
	_, cmd = send(t, screen, "r")
	if cmd == nil {
		t.Fatal("r on the Doctor tab runs the checks again")
	}
	cmd()
	if *runs != 2 {
		t.Errorf("runs = %d, want 2", *runs)
	}
}

func TestDoctorTabAlarmsOnlyOnFailures(t *testing.T) {
	screen, _ := doctorModel(t)
	if screen.doctorProblems() != 0 || screen.buildDoctorTab().alarm {
		t.Error("no alarm before the checks ran")
	}
	model, _ := screen.Update(doctorMsg{findings: doctorFindings()})
	if problems := model.(manageModel).doctorProblems(); problems != 1 {
		t.Errorf("problems = %d, want 1 (a warning is not a failure)", problems)
	}
}

func TestDoctorTabWithoutAnActionStaysIdle(t *testing.T) {
	model, _ := newManageModel(context.Background(), func() (statusReport, error) { return sampleReport(), nil }, manageActions{}, nil).Update(reportMsg{report: sampleReport()})
	screen, cmd := send(t, model.(manageModel), "4")
	if cmd != nil || screen.doctor.running {
		t.Error("nothing to run without a doctor action")
	}
}
