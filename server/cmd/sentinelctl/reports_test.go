package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/ctlapi"
)

// reported has the agent say, now and a few times in the past, how its host and its backups are.
func (p *plant) reported(name string) {
	p.t.Helper()
	now := time.Now()
	for step := range 6 {
		report := agentapi.Report{
			Host: agentapi.HostStatus{
				CPUPercent: float64(step * 15), Load: [3]float64{1, 1, 1},
				Memory: agentapi.MemoryStatus{TotalBytes: 100, AvailableBytes: 40},
				Disks:  []agentapi.DiskStatus{{Mount: "/", TotalBytes: 100, FreeBytes: 20}},
			},
			Backups: []agentapi.BackupStatus{{Job: "db", Repository: "s3", FinishedAt: now.Add(-time.Duration(5-step) * time.Hour), Success: step != 3, Result: map[bool]string{true: "ok", false: "FAILED: disk full"}[step != 3], BytesAdded: 1 << 20}},
		}
		if err := p.app.Report.Record(context.Background(), name, report, now.Add(-time.Duration(5-step)*10*time.Minute)); err != nil {
			p.t.Fatal(err)
		}
	}
}

func TestReportHostShowsTheSummaryAndGraphs(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	if out, err := p.run("", "report", "host", "web01"); err != nil || !strings.Contains(out, "chưa có mẫu nào") {
		t.Errorf("no history yet: %v\n%s", err, out)
	}
	p.reported("web01")
	out, err := p.run("", "report", "host", "web01", "--hours", "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"web01, 2 giờ qua, 6 mẫu", "CPU", "RAM", "60%", "Ổ đĩa đầy nhất", "80%", "▁"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	var history ctlapi.HostReport
	out, _ = p.run("", "report", "host", "web01", "-o", "json")
	if err := json.Unmarshal([]byte(out), &history); err != nil || history.Summary.Samples != 6 || len(history.Points) == 0 {
		t.Errorf("json: %v %s", err, out)
	}
}

func TestReportBackupsListsTheJobsAndTheFailures(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	p.reported("web01")
	out, err := p.run("", "report", "backups", "--days", "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"web01", "db", "s3", "FAILED: disk full", "Các lần lỗi"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	var history ctlapi.BackupReport
	out, _ = p.run("", "report", "backups", "--server", "web01", "-o", "json")
	if err := json.Unmarshal([]byte(out), &history); err != nil || len(history.Jobs) != 1 || history.Jobs[0].Runs != 6 || history.Jobs[0].Failures != 1 || len(history.Failed) != 1 {
		t.Errorf("json: %v %+v", err, history)
	}
}

func TestReportsRefuseWhatTheyCannotAnswer(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	for name, args := range map[string][]string{
		"a server that is not there": {"report", "host", "nobody"},
		"no server":                  {"report", "host"},
		"too many hours":             {"report", "host", "web01", "--hours", "100000"},
		"zero hours":                 {"report", "host", "web01", "--hours", "0"},
		"too many days":              {"report", "backups", "--days", "31"},
		"backups of nobody":          {"report", "backups", "--server", "nobody"},
	} {
		if out, err := p.run("", args...); err == nil {
			t.Errorf("%s: %s", name, out)
		}
	}
}
