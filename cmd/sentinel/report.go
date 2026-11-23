package main

import (
	"context"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/store"
)

// statusReport is everything `sentinel status` and the terminal dashboard
// show, read from the store. It is also the `--output json` format.
type statusReport struct {
	Server      string        `json:"server"`
	GeneratedAt time.Time     `json:"generated_at"`
	Alerts      []alertEntry  `json:"alerts"`
	Backups     []backupEntry `json:"backups"`
	Checks      []checkEntry  `json:"checks"`
}

type alertEntry struct {
	Key     string    `json:"key"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

type backupEntry struct {
	Job        string    `json:"job"`
	Repository string    `json:"repository"`
	FinishedAt time.Time `json:"finished_at"`
	Success    bool      `json:"success"`
	SnapshotID string    `json:"snapshot_id,omitempty"`
	BytesAdded int64     `json:"bytes_added"`
	TotalBytes int64     `json:"total_bytes"`
	Result     string    `json:"result"` // "ok", "ok (warning: ...)" or "FAILED: ..."
	Warning    string    `json:"warning,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type checkEntry struct {
	Name      string    `json:"name"`
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latency_ms"`
	At        time.Time `json:"at"`
	Error     string    `json:"error,omitempty"`
}

func buildStatusReport(ctx context.Context, cfg *config.Config, st *store.Store, now time.Time) (statusReport, error) {
	report := statusReport{
		Server: cfg.ServerName, GeneratedAt: now,
		Alerts: []alertEntry{}, Backups: []backupEntry{}, Checks: []checkEntry{},
	}
	openAlerts, err := st.Alerts(ctx, true)
	if err != nil {
		return report, err
	}
	for _, alert := range openAlerts {
		report.Alerts = append(report.Alerts, alertEntry{Key: alert.Key, Message: alert.Message, Since: alert.Since})
	}
	lastRuns, err := st.LatestBackups(ctx)
	if err != nil {
		return report, err
	}
	for _, run := range lastRuns {
		entry := backupEntry{
			Job: run.Job, Repository: run.Repository, FinishedAt: run.FinishedAt, Success: run.Success,
			SnapshotID: run.SnapshotID, BytesAdded: run.BytesAdded, TotalBytes: run.TotalBytes, Result: runResult(run),
		}
		if run.Success {
			entry.Warning = run.Error
		} else {
			entry.Error = run.Error
		}
		report.Backups = append(report.Backups, entry)
	}
	lastChecks, err := st.LatestChecks(ctx)
	if err != nil {
		return report, err
	}
	for _, check := range lastChecks {
		report.Checks = append(report.Checks, checkEntry{
			Name: check.Name, OK: check.OK, LatencyMs: check.Latency.Milliseconds(), At: check.At, Error: check.Error,
		})
	}
	return report, nil
}
