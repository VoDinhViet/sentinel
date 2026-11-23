// Package status builds the report that `sentinel status` prints and the manage
// screen shows, read from the store. Report is also the `--output json` format.
package status

import (
	"context"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/store"
	"sentinel/internal/termtext"
)

type Report struct {
	Server      string    `json:"server"`
	GeneratedAt time.Time `json:"generated_at"`
	Alerts      []Alert   `json:"alerts"`
	Jobs        []Job     `json:"jobs"`
	Checks      []Check   `json:"checks"`
}

// Alert is an open alert.
type Alert struct {
	Key     string    `json:"key"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

// Job is the latest run of a job into one repository.
type Job struct {
	Job        string    `json:"job"`
	Repository string    `json:"repository"`
	FinishedAt time.Time `json:"finished_at"`
	Success    bool      `json:"success"`
	SnapshotID string    `json:"snapshot_id,omitempty"`
	BytesAdded int64     `json:"bytes_added"`
	TotalBytes int64     `json:"total_bytes"`
	Result     string    `json:"result"` // "ok", "ok (warning: ...)" or "FAILED: ..."
	// Recent is the outcome of the latest runs of this job and repository,
	// oldest first: "ok", "warning" or "failed".
	Recent  []string `json:"recent"`
	Warning string   `json:"warning,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Check is the latest result of an uptime check.
type Check struct {
	Name      string    `json:"name"`
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latency_ms"`
	At        time.Time `json:"at"`
	Error     string    `json:"error,omitempty"`
}

// recentRuns is how many past runs of each job the report carries.
const recentRuns = 8

// jobRepo keys the runs of one job into one repository.
type jobRepo struct{ job, repository string }

// Build reads the store and describes the server's state at now.
func Build(ctx context.Context, cfg *config.Config, st *store.Store, now time.Time) (Report, error) {
	report := Report{
		Server: cfg.ServerName, GeneratedAt: now,
		Alerts: []Alert{}, Jobs: []Job{}, Checks: []Check{},
	}
	openAlerts, err := st.Alerts(ctx, true)
	if err != nil {
		return report, err
	}
	for _, alert := range openAlerts {
		report.Alerts = append(report.Alerts, Alert{Key: alert.Key, Message: termtext.Clean(alert.Message), Since: alert.Since})
	}
	latestRuns, err := st.LatestBackups(ctx)
	if err != nil {
		return report, err
	}
	recent, err := st.RecentBackups(ctx, recentRuns)
	if err != nil {
		return report, err
	}
	outcomesByJobRepo := map[jobRepo][]string{}
	for _, run := range recent {
		key := jobRepo{run.Job, run.Repository}
		outcomesByJobRepo[key] = append(outcomesByJobRepo[key], run.Outcome())
	}
	for _, run := range latestRuns {
		job := Job{
			Recent: outcomesByJobRepo[jobRepo{run.Job, run.Repository}],
			Job:    run.Job, Repository: run.Repository, FinishedAt: run.FinishedAt, Success: run.Success,
			SnapshotID: run.SnapshotID, BytesAdded: run.BytesAdded, TotalBytes: run.TotalBytes, Result: termtext.Clean(run.Summary()),
		}
		if run.Success {
			job.Warning = termtext.Clean(run.Error)
		} else {
			job.Error = termtext.Clean(run.Error)
		}
		report.Jobs = append(report.Jobs, job)
	}
	latestChecks, err := st.LatestChecks(ctx)
	if err != nil {
		return report, err
	}
	for _, result := range latestChecks {
		report.Checks = append(report.Checks, Check{
			Name: result.Name, OK: result.OK, LatencyMs: result.Latency.Milliseconds(), At: result.At, Error: termtext.Clean(result.Error),
		})
	}
	return report, nil
}
