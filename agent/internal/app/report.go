package app

import (
	"context"
	"fmt"
	"time"

	"sentinel/agent/internal/restic"
	"sentinel/agent/internal/status"
	"sentinel/pkg/agentapi"
)

// ControlPlaneReport is what this agent tells the control plane: how the machine is, how each backup
// and check stands, and which alerts are open. version is the release of this binary.
func (s *Service) ControlPlaneReport(ctx context.Context, version string, now time.Time) (agentapi.Report, error) {
	current, err := s.Status(ctx, now)
	if err != nil {
		return agentapi.Report{}, fmt.Errorf("read the status: %w", err)
	}
	return reportOf(version, current, s.Host(ctx).Status()), nil
}

// reportOf puts a status and a reading of the machine in the form of the wire (see agentapi). The text
// goes to another machine, so what restic or a backend printed is redacted first: it can quote a
// repository address with its keys.
func reportOf(version string, current status.Report, host agentapi.HostStatus) agentapi.Report {
	report := agentapi.Report{Version: version, Host: host}
	for _, job := range current.Jobs {
		report.Backups = append(report.Backups, agentapi.BackupStatus{
			Job: job.Job, Repository: job.Repository, FinishedAt: job.FinishedAt, Success: job.Success,
			Result: restic.Redact(job.Result), BytesAdded: job.BytesAdded, TotalBytes: job.TotalBytes,
		})
	}
	for _, check := range current.Checks {
		report.Checks = append(report.Checks, agentapi.CheckStatus{
			Name: check.Name, OK: check.OK, LatencyMs: check.LatencyMs, At: check.At, Error: restic.Redact(check.Error),
		})
	}
	for _, open := range current.Alerts {
		report.Alerts = append(report.Alerts, agentapi.AlertStatus{Key: open.Key, Message: restic.Redact(open.Message), Since: open.Since})
	}
	return report.Clean()
}
