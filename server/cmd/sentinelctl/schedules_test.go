package main

import (
	"encoding/json"
	"strings"
	"testing"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/ctlapi"
)

func TestAScheduleIsAddedListedAndRemoved(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	out, err := p.run("", "schedule", "add", "web01", "0 3 * * *", "backup.start", "job=db")
	if err != nil || !strings.Contains(out, "Đã thêm lịch #1: backup.start trên web01") || !strings.Contains(out, "lần tới") {
		t.Fatalf("add: %v\n%s", err, out)
	}
	out, _ = p.run("", "schedule", "ls")
	for _, want := range []string{"web01", "0 3 * * *", "backup.start", "job=db", "chưa chạy"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls lacks %q:\n%s", want, out)
		}
	}
	var schedules []ctlapi.Schedule
	out, _ = p.run("", "schedule", "ls", "-o", "json", "--server", "web01")
	if err := json.Unmarshal([]byte(out), &schedules); err != nil || len(schedules) != 1 || schedules[0].Next == nil {
		t.Errorf("json: %v %s", err, out)
	}
	if _, err := p.run("", "schedule", "rm", "1"); err != nil {
		t.Fatal(err)
	}
	if out, _ = p.run("", "schedule", "ls"); !strings.Contains(out, "Chưa có lịch nào") {
		t.Errorf("after rm:\n%s", out)
	}
	if _, err := p.run("", "schedule", "rm", "1"); err == nil {
		t.Error("removing a schedule twice worked")
	}
}

func TestAScheduleThatIsNotValidIsRefused(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	for name, args := range map[string][]string{
		"a timetable that is not one": {"schedule", "add", "web01", "every day", "doctor.run"},
		"an action not in the table":  {"schedule", "add", "web01", "@daily", "shell.run", "cmd=id"},
		"a missing parameter":         {"schedule", "add", "web01", "@daily", "backup.start"},
		"a server that is not there":  {"schedule", "add", "nobody", "@daily", "doctor.run"},
		"a bad number":                {"schedule", "rm", "x"},
	} {
		if out, err := p.run("", args...); err == nil {
			t.Errorf("%s: %s", name, out)
		}
	}
}

func TestAScheduleOfAHardToUndoActionAsksLikeRunDoes(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	if _, err := p.run("", "schedule", "add", "web01", "@weekly", agentapi.ActionOSApplyUpdates); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("without a terminal or --yes: %v", err)
	}
	if _, err := p.run("", "schedule", "add", "web01", "@weekly", agentapi.ActionOSApplyUpdates, "--yes"); err != nil {
		t.Errorf("--yes: %v", err)
	}
}

func TestAMaintenanceWindowChangesTheStatusOfItsServer(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	p.activeServer("db01")
	status := func(name string) string {
		var agents []ctlapi.Agent
		out, _ := p.run("", "server", "ls", "-o", "json")
		if err := json.Unmarshal([]byte(out), &agents); err != nil {
			t.Fatal(err)
		}
		for _, agent := range agents {
			if agent.Name == name {
				return agent.Status
			}
		}
		return ""
	}
	before := status("web01")
	out, err := p.run("", "maintenance", "add", "web01", "--for", "2h", "--reason", "nâng kernel")
	if err != nil || !strings.Contains(out, "Đã mở cửa sổ bảo trì #1 cho web01") {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if got := status("web01"); got != "MAINTENANCE" || before == "MAINTENANCE" {
		t.Errorf("web01 was %s, is %s", before, got)
	}
	if got := status("db01"); got == "MAINTENANCE" {
		t.Error("a window of web01 covered db01")
	}
	if out, _ = p.run("", "maintenance", "ls"); !strings.Contains(out, "web01") || !strings.Contains(out, "đang mở") || !strings.Contains(out, "nâng kernel") {
		t.Errorf("ls:\n%s", out)
	}
	if out, _ = p.run("", "server", "ls"); !strings.Contains(out, "MAINTENANCE (bảo trì)") {
		t.Errorf("server ls:\n%s", out)
	}
	if _, err := p.run("", "maintenance", "rm", "1"); err != nil {
		t.Fatal(err)
	}
	if got := status("web01"); got == "MAINTENANCE" {
		t.Error("the status stayed after the window was removed")
	}
	if _, err := p.run("", "maintenance", "add", "--all", "--for", "30m"); err != nil {
		t.Fatal(err)
	}
	if status("web01") != "MAINTENANCE" || status("db01") != "MAINTENANCE" {
		t.Error("a window for all did not cover every server")
	}
}

func TestAMaintenanceWindowIsCheckedWhenItIsAsked(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	for name, args := range map[string][]string{
		"no time":                 {"maintenance", "add", "web01"},
		"no server and not all":   {"maintenance", "add", "--for", "1h"},
		"a server and all":        {"maintenance", "add", "web01", "--all", "--for", "1h"},
		"a start that is no time": {"maintenance", "add", "web01", "--for", "1h", "--from", "tomorrow"},
		"too long":                {"maintenance", "add", "web01", "--for", "1000h"},
		"a server not there":      {"maintenance", "add", "nobody", "--for", "1h"},
	} {
		if out, err := p.run("", args...); err == nil {
			t.Errorf("%s: %s", name, out)
		}
	}
	if out, err := p.run("", "maintenance", "add", "web01", "--for", "1h", "--from", "2099-01-01 02:00"); err != nil {
		t.Errorf("a window in the future: %v %s", err, out)
	}
	if out, _ := p.run("", "maintenance", "ls"); !strings.Contains(out, "sắp tới") {
		t.Errorf("a future window:\n%s", out)
	}
}
