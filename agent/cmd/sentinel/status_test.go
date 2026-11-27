package main

import (
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/status"
)

func TestDaemonStateSaysWhatToDoWhenItIsNotRunning(t *testing.T) {
	running, stopped := true, false
	for name, tc := range map[string]struct {
		running *bool
		want    string
	}{
		"running":   {&running, "running"},
		"stopped":   {&stopped, "NOT RUNNING"},
		"not known": {nil, "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := daemonState(tc.running); !strings.HasPrefix(got, tc.want) {
				t.Errorf("daemonState = %q, want it to start with %q", got, tc.want)
			}
		})
	}
	if got := daemonState(&stopped); !strings.Contains(got, "systemctl start sentinel") {
		t.Errorf("daemonState = %q, want the command to start it", got)
	}
}

func TestPrintStatusShowsAlertsJobsAndChecks(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	report := status.Report{
		Server: "web-01", GeneratedAt: now,
		Alerts: []status.Alert{{Key: "backup:appdb:s3", Message: "appdb has not backed up for 3 days", Since: now.Add(-time.Hour)}},
		Jobs: []status.Job{
			{Job: "appdb", Repository: "s3", FinishedAt: now.Add(-time.Hour), Success: true, SnapshotID: "0123456789abcdef", Result: "ok"},
			{Job: "files", Repository: "local", FinishedAt: now.Add(-2 * time.Hour), Error: "restic exited 1", Result: "FAILED: restic exited 1"},
		},
		Checks: []status.Check{{Name: "api", OK: false, At: now, Error: "connection refused"}},
	}
	var output strings.Builder
	if err := printStatus(&output, report); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Server: web-01", "Open alerts: 1", "appdb", "01234567", "FAILED: restic exited 1", "DOWN: connection refused"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, output.String())
		}
	}
}
