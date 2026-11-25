// Package status builds the report that `sentinel status` prints and the manage
// screen shows, read from the store. Report is also the `--output json` format.
package status

import (
	"context"
	"time"

	"sentinel/internal/backup"
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
	// Running are the backups in progress right now, from any process (daemon, CLI, detached).
	Running []Running `json:"running"`
}

// Running is a backup in progress.
type Running struct {
	Job        string    `json:"job"`
	Repository string    `json:"repository"`
	Phase      string    `json:"phase"` // "backup" or "retention"
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	BytesDone  int64     `json:"bytes_done"`
	// BytesTotal is what restic knows, or for a database dump the size of the last backup
	// (Estimated); 0 when unknown. Percent is absent when there is nothing to measure against.
	BytesTotal       int64    `json:"bytes_total,omitempty"`
	Percent          *float64 `json:"percent,omitempty"`
	Estimated        bool     `json:"estimated,omitempty"`
	BytesPerSecond   float64  `json:"bytes_per_second,omitempty"`
	RemainingSeconds int64    `json:"remaining_seconds,omitempty"`
	// Stalled: no new data for ten minutes. WillExceedTimeout: at this speed it ends after the
	// job's timeout and would be cut off.
	Stalled           bool   `json:"stalled,omitempty"`
	WillExceedTimeout bool   `json:"will_exceed_timeout,omitempty"`
	TimeoutSeconds    int64  `json:"timeout_seconds,omitempty"`
	Summary           string `json:"summary"`
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
	// RecentLatencyMs and RecentOK are the latest results of the check, oldest first: the
	// latency of each run and whether it was up.
	RecentLatencyMs []int64 `json:"recent_latency_ms,omitempty"`
	RecentOK        []bool  `json:"recent_ok,omitempty"`
}

// recentRuns is how many past runs of each job, and past results of each uptime check, the report carries.
const recentRuns = 8

// jobRepo keys the runs of one job into one repository.
type jobRepo struct{ job, repository string }

// Build reads the store and describes the server's state at now.
func Build(ctx context.Context, cfg *config.Config, st *store.Store, now time.Time) (Report, error) {
	report := Report{
		Server: cfg.ServerName, GeneratedAt: now,
		Alerts: []Alert{}, Jobs: []Job{}, Checks: []Check{}, Running: []Running{},
	}
	if cfg.StorePath != "" {
		for _, progress := range backup.ReadRunning(backup.ProgressDir(cfg.StorePath), now) {
			running := Running{
				Job: progress.Job, Repository: progress.Repository, Phase: progress.Phase, PID: progress.PID, StartedAt: progress.StartedAt,
				BytesDone: progress.BytesDone, BytesTotal: progress.BytesTotal, Estimated: progress.Estimated, BytesPerSecond: progress.Rate,
				RemainingSeconds: int64(progress.Remaining.Seconds()), Stalled: progress.Stalled(now), WillExceedTimeout: progress.WillExceedTimeout(),
				TimeoutSeconds: int64(progress.Timeout.Seconds()), Summary: progress.Summary(),
			}
			if progress.Percent >= 0 {
				percent := progress.Percent
				running.Percent = &percent
			}
			report.Running = append(report.Running, running)
		}
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
	recentChecks, err := st.RecentChecks(ctx, recentRuns)
	if err != nil {
		return report, err
	}
	recentByName := map[string][]store.CheckResult{}
	for _, result := range recentChecks {
		recentByName[result.Name] = append(recentByName[result.Name], result)
	}
	for _, result := range latestChecks {
		check := Check{
			Name: result.Name, OK: result.OK, LatencyMs: result.Latency.Milliseconds(), At: result.At, Error: termtext.Clean(result.Error),
		}
		for _, past := range recentByName[result.Name] {
			check.RecentLatencyMs = append(check.RecentLatencyMs, past.Latency.Milliseconds())
			check.RecentOK = append(check.RecentOK, past.OK)
		}
		report.Checks = append(report.Checks, check)
	}
	return report, nil
}
