package app

import (
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/status"
	"sentinel/pkg/agentapi"
)

func TestReportOfCarriesTheStatusAndKeepsSecretsOut(t *testing.T) {
	now := time.Now()
	current := status.Report{
		Jobs:   []status.Job{{Job: "db", Repository: "s3", FinishedAt: now, Success: false, Result: "FAILED: s3 error AWS_SECRET_ACCESS_KEY=abc123 denied", BytesAdded: 5}},
		Checks: []status.Check{{Name: "site", OK: false, At: now, Error: "token=hunter2 refused"}},
		Alerts: []status.Alert{{Key: "backup:db:s3", Message: "failed password=hunter2", Since: now}},
	}
	report := reportOf("1.2.3", current, agentapi.HostStatus{Hostname: "web01"}, config.Allow{Doctor: true})
	if got := strings.Join(report.Allow, ","); got != "doctor.run" {
		t.Errorf("allow = %q: the report says which actions the allowlist lets run", got)
	}
	if report.Version != "1.2.3" || report.Host.Hostname != "web01" || len(report.Backups) != 1 || report.Backups[0].BytesAdded != 5 {
		t.Fatalf("report = %+v", report)
	}
	for _, text := range []string{report.Backups[0].Result, report.Checks[0].Error, report.Alerts[0].Message} {
		if strings.Contains(text, "abc123") || strings.Contains(text, "hunter2") {
			t.Errorf("a secret is left in %q", text)
		}
	}
}

func TestReportOfAnEmptyStatusHasListsNotNulls(t *testing.T) {
	report := reportOf("v", status.Report{}, agentapi.HostStatus{}, config.Allow{})
	if report.Backups == nil || report.Checks == nil || report.Alerts == nil || report.Allow == nil {
		t.Errorf("a list is nil: %+v", report)
	}
}
