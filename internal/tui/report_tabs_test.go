package tui

import (
	"testing"
	"time"

	"sentinel/internal/status"
)

// A report from another machine: no config, no schedules, only what the report says.
func remoteReport() status.Report {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	return status.Report{
		Server: "db-01", GeneratedAt: now,
		Alerts: []status.Alert{{Key: "stale:shop", Message: "no backup for 3 days", Since: now.Add(-time.Hour)}},
		Jobs: []status.Job{
			{Job: "appdb", Repository: "s3", FinishedAt: now.Add(-time.Hour), Success: true, SnapshotID: "0123456789abcdef", Result: "ok"},
			{Job: "shop", Repository: "s3", FinishedAt: now.Add(-2 * time.Hour), Error: "restic exited 1\nsecond line", Result: "FAILED: restic exited 1"},
		},
		Checks:  []status.Check{{Name: "site", OK: true, LatencyMs: 20, At: now}, {Name: "db-port", OK: false, At: now, Error: "refused"}},
		Running: []status.Running{{Job: "appdb", Repository: "s3", Phase: "backup", BytesDone: 1 << 20, StartedAt: now.Add(-time.Minute)}},
	}
}

func TestBackupsTabOfAReportAloneNeedsNoConfig(t *testing.T) {
	tab := backupsTabOf(remoteReport(), backupKnowledge{})
	if len(tab.lines) != 2 || !tab.alarm || tab.count != 2 {
		t.Fatalf("lines %d, alarm %v, count %d", len(tab.lines), tab.alarm, tab.count)
	}
	appdb, shop := tab.lines[0], tab.lines[1]
	if !appdb.running || appdb.severity != sevRun {
		t.Errorf("appdb is running now: %+v", appdb)
	}
	if shop.severity != sevBad || shop.cells[3] != "LỖI" || shop.sub != "restic exited 1" {
		t.Errorf("shop failed: %+v", shop)
	}
	if shop.cells[8] != "" {
		t.Errorf("next run = %q, a report knows no schedule", shop.cells[8])
	}
	if len(appdb.fields) != 2 || appdb.fields[0] != [2]string{"Repository", "s3"} {
		t.Errorf("fields = %v", appdb.fields)
	}
}

func TestBackupsTabShowsTheBackupsTheConfigDescribesThatNeverRan(t *testing.T) {
	known := backupKnowledge{jobRepositories: []jobRepository{{"appdb", "s3"}, {"files", "local"}}}
	tab := backupsTabOf(remoteReport(), known)
	if len(tab.lines) != 3 || tab.lines[2].job != "files" || tab.lines[2].cells[3] != cellNeverRun || tab.lines[2].severity != sevWarn {
		t.Errorf("lines = %+v", tab.lines)
	}
}

func TestChecksAndAlertsTabsOfAReport(t *testing.T) {
	checks := checksTabOf(remoteReport())
	if len(checks.lines) != 2 || checks.lines[0].cells[1] != "UP" || checks.lines[1].cells[1] != "DOWN" || !checks.alarm {
		t.Errorf("checks = %+v", checks)
	}
	alerts := alertsTabOf(remoteReport())
	if len(alerts.lines) != 1 || alerts.lines[0].cells[0] != "stale:shop" || !alerts.alarm || alerts.count != 1 {
		t.Errorf("alerts = %+v", alerts)
	}
	if quiet := alertsTabOf(status.Report{}); quiet.alarm || len(quiet.lines) != 0 {
		t.Errorf("a quiet report has no alarm: %+v", quiet)
	}
}
