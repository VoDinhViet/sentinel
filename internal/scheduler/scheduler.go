// Package scheduler runs sentinel's periodic work: backups on their cron
// schedules, repository checks, stale-backup detection and interval tasks
// such as uptime checks and resource monitoring.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"sentinel/internal/alert"
	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/engine"
	"sentinel/internal/notify"
	"sentinel/internal/schedule"
	"sentinel/internal/store"
)

// Task is work repeated at a fixed interval.
type Task struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context)
}

type Daemon struct {
	Config  *config.Config
	Store   *store.Store
	Alerts  *alert.Manager
	Backups *backup.Runner
	Tasks   []Task
	// ShutdownTimeout bounds how long Run waits for jobs after cancelling them.
	ShutdownTimeout time.Duration
	// HistoryRetention is how long check results and metrics are kept.
	HistoryRetention time.Duration

	started time.Time
	busy    atomic.Int32 // backups and repository checks in progress
	// running holds one mutex per job name, so a job never runs twice at once
	// (a "backup now" overlapping a scheduled run would fight over the repository
	// lock and dump the same database twice).
	running sync.Map // job name -> *sync.Mutex
}

// ErrAlreadyRunning is the Error of the run RunBackup returns, without
// recording it, when the job is already running.
const ErrAlreadyRunning = backup.AlreadyRunning

// Busy reports whether a backup or repository check is running, so a config
// reload can wait instead of interrupting it.
func (d *Daemon) Busy() bool { return d.busy.Load() > 0 }

// Run schedules all work and blocks until ctx is cancelled. It then stops
// scheduling, cancels running jobs (restic gets SIGINT so it can release its
// lock) and waits for them to exit.
func (d *Daemon) Run(ctx context.Context) error {
	d.started = time.Now()
	d.sweepStaleSecretFiles()
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()

	logger := slogAdapter{}
	c := cron.New(cron.WithChain(cron.Recover(logger), cron.SkipIfStillRunning(logger)))

	for i := range d.Config.Backups {
		job := &d.Config.Backups[i]
		if job.Schedule == "" {
			continue
		}
		if _, err := c.AddFunc(job.Schedule, func() { d.RunBackup(jobCtx, job) }); err != nil {
			return fmt.Errorf("schedule backup %s: %w", job.Name, err)
		}
		slog.Info("scheduled backup", "job", job.Name, "schedule", job.Schedule)
	}
	if d.Config.RepoCheckSchedule != "" {
		if _, err := c.AddFunc(d.Config.RepoCheckSchedule, func() { d.CheckRepositories(jobCtx) }); err != nil {
			return fmt.Errorf("schedule repo check: %w", err)
		}
	}

	tasks := append([]Task{
		{Name: "stale-backups", Every: 15 * time.Minute, Run: d.CheckStale},
		{Name: "prune-history", Every: 24 * time.Hour, Run: d.pruneHistory},
	}, d.Tasks...)
	for _, t := range tasks {
		c.Schedule(cron.Every(t.Every), cron.FuncJob(func() { t.Run(jobCtx) }))
		slog.Debug("scheduled task", "task", t.Name, "every", t.Every)
	}

	c.Start()
	slog.Info("sentinel started", "server", d.Config.ServerName, "backups", len(d.Config.Backups), "tasks", len(tasks))
	<-ctx.Done()

	slog.Info("shutting down, waiting for running jobs")
	stopped := c.Stop()
	cancelJobs()
	timeout := d.ShutdownTimeout
	if timeout == 0 {
		timeout = time.Minute
	}
	select {
	case <-stopped.Done():
		slog.Info("stopped")
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("jobs still running after %s", timeout)
	}
}

func backupKey(job, repo string) string { return "backup:" + job + ":" + repo }

// RunBackup runs one job and raises or resolves an alert per repository.
//
// If the job is already running it does nothing and returns one unrecorded run
// whose Error is ErrAlreadyRunning; no alert is raised for it.
func (d *Daemon) RunBackup(ctx context.Context, job *config.Backup) []store.BackupRun {
	jobLock, _ := d.running.LoadOrStore(job.Name, new(sync.Mutex))
	if !jobLock.(*sync.Mutex).TryLock() {
		slog.Warn("backup skipped, already running", "job", job.Name)
		return []store.BackupRun{{Job: job.Name, Error: ErrAlreadyRunning}}
	}
	defer jobLock.(*sync.Mutex).Unlock()

	d.busy.Add(1)
	defer d.busy.Add(-1)
	runs := d.Backups.Run(ctx, job)
	if len(runs) == 1 && runs[0].Error == backup.AlreadyRunning {
		return runs // another sentinel process holds the job: nothing ran, nothing to alert
	}
	if ctx.Err() != nil {
		// Shutting down: the run was interrupted, not broken.
		return runs
	}
	var okRepos []string
	for _, r := range runs {
		key := backupKey(job.Name, r.Repository)
		if r.Success {
			d.Alerts.Resolve(ctx, key, fmt.Sprintf("Backup %s to %s succeeded", job.Name, r.Repository), "")
			okRepos = append(okRepos, fmt.Sprintf("%s: snapshot %.8s, +%d bytes", r.Repository, r.SnapshotID, r.BytesAdded))
			// The snapshot is safe, but retention failed: warn separately from a failed backup.
			retentionKey := "retention:" + job.Name + ":" + r.Repository
			if r.Error != "" {
				d.Alerts.Fire(ctx, retentionKey, notify.Warning,
					fmt.Sprintf("Retention for %s in %s failed", job.Name, r.Repository), r.Error)
			} else {
				d.Alerts.Resolve(ctx, retentionKey, fmt.Sprintf("Retention for %s in %s succeeded", job.Name, r.Repository), "")
			}
			continue
		}
		d.Alerts.Fire(ctx, key, notify.Critical,
			fmt.Sprintf("Backup %s to %s failed", job.Name, r.Repository), r.Error)
	}
	if d.Config.Alert.NotifySuccess && len(okRepos) > 0 {
		d.Alerts.Info(ctx, "Backup "+job.Name+" completed", strings.Join(okRepos, "\n"))
	}
	return runs
}

// CheckRepositories runs `restic check` on every repository used by a job.
func (d *Daemon) CheckRepositories(ctx context.Context) {
	d.busy.Add(1)
	defer d.busy.Add(-1)
	used := map[string]bool{}
	for _, b := range d.Config.Backups {
		for _, r := range b.Repositories {
			used[r] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(used)) {
		rc, err := d.Backups.Restic(name)
		if err == nil {
			err = rc.Check(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		key := "repo-check:" + name
		if err != nil {
			d.Alerts.Fire(ctx, key, notify.Critical, "Repository check failed: "+name, err.Error())
		} else {
			d.Alerts.Resolve(ctx, key, "Repository check passed: "+name, "")
		}
	}
}

// CheckStale alerts on jobs that have not succeeded for too long.
func (d *Daemon) CheckStale(ctx context.Context) {
	now := time.Now()
	for i := range d.Config.Backups {
		job := &d.Config.Backups[i]
		last, ok, err := d.Store.LastSuccessfulBackup(ctx, job.Name)
		if err != nil {
			slog.Error("stale check", "job", job.Name, "error", err)
			continue
		}
		if !ok {
			last = time.Time{}
		}
		stale, limit := IsStale(job, last, d.started, now)
		key := "backup-stale:" + job.Name
		if stale {
			since := "never succeeded"
			if ok {
				since = "last success " + last.Format("2006-01-02 15:04")
			}
			d.Alerts.Fire(ctx, key, notify.Warning,
				fmt.Sprintf("Backup %s is overdue", job.Name),
				fmt.Sprintf("No successful backup within %s (%s).", limit, since))
		} else if limit > 0 {
			d.Alerts.Resolve(ctx, key, fmt.Sprintf("Backup %s is up to date", job.Name), "")
		}
	}
}

// IsStale reports whether job's last success (zero if never) is older than
// twice the longest gap in its schedule plus its timeout. Jobs that never ran
// are measured from daemon start so a fresh install is not instantly stale.
// Unscheduled jobs are never stale (limit 0).
func IsStale(job *config.Backup, lastSuccess, started, now time.Time) (stale bool, limit time.Duration) {
	if job.Schedule == "" {
		return false, 0
	}
	maxGap, ok := schedule.MaxGap(job.Schedule, now)
	if !ok {
		return false, 0
	}
	limit = 2*maxGap + job.Timeout
	ref := lastSuccess
	if ref.IsZero() {
		ref = started
	}
	return now.Sub(ref) > limit, limit
}

func (d *Daemon) pruneHistory(ctx context.Context) {
	keep := d.HistoryRetention
	if keep == 0 {
		keep = 30 * 24 * time.Hour
	}
	if err := d.Store.Prune(ctx, time.Now().Add(-keep)); err != nil {
		slog.Error("prune history", "error", err)
	}
}

type slogAdapter struct{}

func (slogAdapter) Info(msg string, kv ...any) { slog.Debug("cron: "+msg, kv...) }
func (slogAdapter) Error(err error, msg string, kv ...any) {
	slog.Error("cron: "+msg, append(kv, "error", err)...)
}

// sweepStaleSecretFiles removes credential files a crashed dump left in the
// temp directory. A file younger than the longest backup timeout may belong to
// a dump running in another sentinel process, so it is kept.
func (d *Daemon) sweepStaleSecretFiles() {
	longest := time.Hour
	for _, job := range d.Config.Backups {
		longest = max(longest, job.Timeout)
	}
	if removed := engine.SweepStaleSecretFiles(longest + time.Hour); removed > 0 {
		slog.Warn("removed stale credential files left by a crashed backup", "count", removed)
	}
}
