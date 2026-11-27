// Package report is the history of what agents say about themselves, and the reports made from it: the host
// as a series of samples, and the backups as a list of runs. The latest report of an agent is kept by
// inventory; this package keeps what came before it, within limits.
package report

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/store"
)

// What is kept, and for how long.
const (
	// SampleEvery is the least time between two samples of one agent: reports come every minute, a history of
	// a month at that rate is a million rows for ten servers, and a graph does not need them.
	SampleEvery = 5 * time.Minute
	// SampleKeep and RunKeep are how long samples and backup runs are kept.
	SampleKeep = 30 * 24 * time.Hour
	RunKeep    = 90 * 24 * time.Hour
	// MaxHours is the longest span a report may ask for: the longest history there is.
	MaxHours = int(SampleKeep / time.Hour)
	// maxFuture is how far ahead of the control plane's clock the end of a backup may be and still be believed.
	maxFuture = 5 * time.Minute
	// MaxRunsPerAgent is the most backup runs kept for one agent: an honest agent has a few per job per day, and a
	// wrong or hostile one must not fill the database. Past it the oldest are dropped.
	MaxRunsPerAgent = 5000
	// maxBytes and maxLoad are what a number of bytes or a load is held to: they came from another machine, and a
	// sum or an average of absurd values would make every report that includes them fail.
	maxBytes = int64(1) << 50
	maxLoad  = 1e6
	// MaxPoints is the most points a host history has: a longer span is averaged into steps.
	MaxPoints = 300
	// maxFailures is how many failed runs a backup report lists.
	maxFailures = 20
)

// Service records and reads the history.
type Service struct {
	store *store.Store
	// Now is the time; nil means the clock.
	Now func() time.Time
}

func New(st *store.Store) *Service { return &Service{store: st} }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Record keeps what a report says for the history: a sample of the host unless one was taken less than
// SampleEvery ago, and each backup run the report names that is not kept yet. The report came from another
// machine, so it is cleaned first, and the clock that counts is the control plane's own.
func (s *Service) Record(ctx context.Context, agent string, report agentapi.Report, now time.Time) error {
	report = report.Clean()
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		if memory := report.Host.Memory; memory.TotalBytes > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO host_samples (agent, at, cpu_percent, load1, mem_percent, disk_percent)
				SELECT ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM host_samples WHERE agent = ? AND at > ?)`,
				agent, now.Unix(), clamp(report.Host.CPUPercent), clampLoad(report.Host.Load[0]), clamp(used(memory.TotalBytes, memory.AvailableBytes)), diskPercent(report.Host.Disks),
				agent, now.Add(-SampleEvery).Unix()); err != nil {
				return fmt.Errorf("record host sample: %w", err)
			}
		}
		for _, run := range report.Backups {
			if run.FinishedAt.IsZero() || run.FinishedAt.After(now.Add(maxFuture)) || run.FinishedAt.Before(now.Add(-RunKeep)) {
				continue
			}
			success := 0
			if run.Success {
				success = 1
			}
			// Only a run newer than the ones kept for the same job and repository: an agent says its latest run in every
			// report, so this is one row per real run, and a made-up past does not pile up.
			if _, err := tx.ExecContext(ctx, `INSERT INTO backup_runs (agent, job, repository, finished_at, success, result, bytes_added)
				SELECT ?, ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM backup_runs WHERE agent = ? AND job = ? AND repository = ? AND finished_at >= ?)`,
				agent, run.Job, run.Repository, run.FinishedAt.Unix(), success, run.Result, min(max(run.BytesAdded, 0), maxBytes),
				agent, run.Job, run.Repository, run.FinishedAt.Unix()); err != nil {
				return fmt.Errorf("record backup run: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM backup_runs WHERE agent = ? AND finished_at < (
			SELECT finished_at FROM backup_runs WHERE agent = ? ORDER BY finished_at DESC LIMIT 1 OFFSET ?)`, agent, agent, MaxRunsPerAgent-1); err != nil {
			return fmt.Errorf("trim backup runs: %w", err)
		}
		return nil
	})
}

// used is the share of total that is not available, in percent.
func used(total, available uint64) float64 {
	if total == 0 || available > total {
		return 0
	}
	return float64(total-available) / float64(total) * 100
}

// diskPercent is the use of the fullest disk.
func diskPercent(disks []agentapi.DiskStatus) float64 {
	fullest := 0.0
	for _, disk := range disks {
		fullest = max(fullest, used(disk.TotalBytes, disk.FreeBytes))
	}
	return fullest
}

// clamp keeps a percentage a number from 0 to 100: it came from another machine.
func clamp(percent float64) float64 {
	if percent != percent { // NaN
		return 0
	}
	return min(max(percent, 0), 100)
}

// clampLoad keeps a load average a number from 0 to maxLoad.
func clampLoad(load float64) float64 {
	if load != load { // NaN
		return 0
	}
	return min(max(load, 0), maxLoad)
}

// Prune deletes the history that is older than it is kept.
func (s *Service) Prune(ctx context.Context, now time.Time) error {
	if _, err := s.store.DB.ExecContext(ctx, `DELETE FROM host_samples WHERE at < ?`, now.Add(-SampleKeep).Unix()); err != nil {
		return fmt.Errorf("delete old samples: %w", err)
	}
	if _, err := s.store.DB.ExecContext(ctx, `DELETE FROM backup_runs WHERE finished_at < ?`, now.Add(-RunKeep).Unix()); err != nil {
		return fmt.Errorf("delete old backup runs: %w", err)
	}
	return nil
}

// Run prunes once an hour until ctx is done.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := s.Prune(ctx, s.now()); err != nil && ctx.Err() == nil {
			slog.Error("prune history", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
