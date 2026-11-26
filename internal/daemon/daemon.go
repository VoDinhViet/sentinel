// Package daemon runs sentinel's long-running work: backups on their cron
// schedules, repository checks, stale-job detection, history pruning and
// interval tasks such as uptime checks.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
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

// retryAlertsEvery is how often alert messages that no notifier took are sent again.
const retryAlertsEvery = 5 * time.Minute

// Task is work repeated at a fixed interval.
type Task struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context)
	// AtStart also runs the task once when the daemon starts, not only after the first interval.
	AtStart bool
}

type Daemon struct {
	Config *config.Config
	Store  *store.Store
	Alerts *alert.Manager
	Runner *backup.Runner
	Tasks  []Task
	// ShutdownWait is how long Run lets running jobs finish on its own after it is asked to
	// stop, before it cancels them. Zero cancels them at once.
	ShutdownWait time.Duration
	// ForceStop, when closed, ends the ShutdownWait early (a second stop signal).
	ForceStop <-chan struct{}
	// ShutdownTimeout bounds how long Run waits for jobs after cancelling them.
	ShutdownTimeout time.Duration
	// HistoryMaxAge is how long check results are kept.
	HistoryMaxAge time.Duration

	started time.Time
	// running holds one mutex per job name, so a job never runs twice at once
	// (a "backup now" overlapping a scheduled run would fight over the repository
	// lock and dump the same database twice).
	running sync.Map // job name -> *sync.Mutex
}

// Run schedules all work and blocks until ctx is cancelled. It then stops
// scheduling, gives running jobs up to ShutdownWait to finish, cancels the rest (restic
// gets SIGINT so it can release its lock) and waits for them to exit.
func (d *Daemon) Run(ctx context.Context) error {
	if d.Runner != nil {
		held, release, err := d.Runner.LockDaemon()
		if err != nil {
			return err
		}
		defer release()
		if !held {
			return errors.New("another sentinel daemon is already running with this state directory")
		}
	}
	d.started = time.Now()
	d.sweepStaleSecretFiles()
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()

	logger := cronLogger{}
	cronRunner := cron.New(cron.WithChain(cron.Recover(logger), cron.SkipIfStillRunning(logger)))

	for i := range d.Config.Jobs {
		job := &d.Config.Jobs[i]
		if schedule.IsManual(job.Schedule) {
			continue
		}
		cronSchedule, err := schedule.Parse(job.Schedule)
		if err != nil {
			return fmt.Errorf("schedule backup %s: %w", job.Name, err)
		}
		cronRunner.Schedule(cronSchedule, cron.FuncJob(func() { d.RunBackup(jobCtx, job) }))
		slog.Info("scheduled backup", "job", job.Name, "schedule", job.Schedule)
	}
	if !schedule.IsManual(d.Config.VerifySchedule) {
		cronSchedule, err := schedule.Parse(d.Config.VerifySchedule)
		if err != nil {
			return fmt.Errorf("schedule repo check: %w", err)
		}
		cronRunner.Schedule(cronSchedule, cron.FuncJob(func() { d.CheckRepositories(jobCtx) }))
	}

	tasks := append([]Task{
		{Name: "stale-backups", Every: 15 * time.Minute, Run: d.CheckStale},
		{Name: "retry-alerts", Every: retryAlertsEvery, Run: d.Alerts.RetryPending},
		{Name: "prune-history", Every: 24 * time.Hour, Run: d.pruneHistory},
	}, d.Tasks...)
	for _, task := range tasks {
		cronRunner.Schedule(cron.Every(task.Every), cron.FuncJob(func() { task.Run(jobCtx) }))
		slog.Debug("scheduled task", "task", task.Name, "every", task.Every)
	}

	var startup sync.WaitGroup
	for _, task := range tasks {
		if task.AtStart {
			startup.Go(func() { task.Run(jobCtx) })
		}
	}
	cronRunner.Start()
	slog.Info("sentinel started", "server", d.Config.ServerName, "backups", len(d.Config.Jobs), "tasks", len(tasks))
	<-ctx.Done()

	slog.Info("shutting down")
	stopped := cronRunner.Stop()
	if d.ShutdownWait > 0 {
		select {
		case <-stopped.Done():
		default:
			slog.Info("waiting for running jobs to finish; stop again to cancel them", "up_to", d.ShutdownWait)
			wait := time.NewTimer(d.ShutdownWait)
			select {
			case <-stopped.Done():
			case <-wait.C:
				slog.Warn("running jobs did not finish in time, cancelling them", "after", d.ShutdownWait)
			case <-d.ForceStop:
				slog.Warn("stopped again, cancelling running jobs")
			}
			wait.Stop()
		}
	}
	cancelJobs()
	startup.Wait() // the tasks started with the daemon end with their context
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

// RunBackup runs one job and raises or resolves an alert per repository.
//
// If the job is already running it does nothing and returns one unrecorded run
// whose Error is backup.AlreadyRunning; no alert is raised for it.
func (d *Daemon) RunBackup(ctx context.Context, job *config.Job) []store.BackupRun {
	jobLock, _ := d.running.LoadOrStore(job.Name, new(sync.Mutex))
	if !jobLock.(*sync.Mutex).TryLock() {
		slog.Warn("backup skipped, already running", "job", job.Name)
		return []store.BackupRun{{Job: job.Name, Error: backup.AlreadyRunning}}
	}
	defer jobLock.(*sync.Mutex).Unlock()

	runs := d.Runner.Run(ctx, job)
	if len(runs) == 1 && runs[0].Error == backup.AlreadyRunning {
		return runs // another sentinel process holds the job: nothing ran, nothing to alert
	}
	if ctx.Err() != nil {
		// Shutting down: the run was interrupted, not broken.
		return runs
	}
	var successLines []string
	for _, run := range runs {
		key := alert.BackupKey(job.Name, run.Repository)
		if run.Error == backup.Stopped {
			continue // somebody stopped it on purpose: neither a failure nor a success
		}
		if run.Success {
			d.Alerts.Resolve(ctx, key, fmt.Sprintf("Backup %s to %s succeeded", job.Name, run.Repository), "")
			successLines = append(successLines, fmt.Sprintf("%s: snapshot %.8s, +%d bytes", run.Repository, run.SnapshotID, run.BytesAdded))
			// The snapshot is safe, but retention failed: warn separately from a failed backup.
			retentionKey := alert.RetentionKey(job.Name, run.Repository)
			if run.Error != "" {
				d.Alerts.Fire(ctx, retentionKey, notify.Warning,
					fmt.Sprintf("Retention for %s in %s failed", job.Name, run.Repository), run.Error)
			} else {
				d.Alerts.Resolve(ctx, retentionKey, fmt.Sprintf("Retention for %s in %s succeeded", job.Name, run.Repository), "")
			}
			continue
		}
		d.Alerts.Fire(ctx, key, notify.Critical,
			fmt.Sprintf("Backup %s to %s failed", job.Name, run.Repository), run.Error)
	}
	if d.Config.Alert.NotifySuccess && len(successLines) > 0 {
		d.Alerts.Info(ctx, "Backup "+job.Name+" completed", strings.Join(successLines, "\n"))
	}
	return runs
}

// verifyTimeout bounds one `restic check`. restic retries a failing backend for a long time and keeps the
// repository locked meanwhile, which blocks the prune of every job that uses it, so a check that hangs must end
// and be reported. It may take as long as the slowest backup is allowed to, since both read the repository,
// and never less than the default job timeout: jobs with short timeouts (a small database) say nothing about
// how long a check of a large repository takes.
func (d *Daemon) verifyTimeout() time.Duration {
	longest := defaultVerifyTimeout
	for _, job := range d.Config.Jobs {
		longest = max(longest, job.Timeout)
	}
	return longest
}

// defaultVerifyTimeout is the default job timeout, and the least a repository check is given.
const defaultVerifyTimeout = 6 * time.Hour

// CheckRepositories runs `restic check` on every repository used by a job.
func (d *Daemon) CheckRepositories(ctx context.Context) {
	usedRepos := map[string]bool{}
	for _, job := range d.Config.Jobs {
		for _, repoName := range job.Repositories {
			usedRepos[repoName] = true
		}
	}
	for _, repoName := range slices.Sorted(maps.Keys(usedRepos)) {
		resticClient, err := d.Runner.Restic(repoName)
		if err == nil {
			verifyCtx, cancel := context.WithTimeout(ctx, d.verifyTimeout())
			err = resticClient.Verify(verifyCtx)
			cancel()
		}
		if ctx.Err() != nil {
			return
		}
		key := alert.VerifyKey(repoName)
		if err != nil {
			d.Alerts.Fire(ctx, key, notify.Critical, "Repository check failed: "+repoName, err.Error())
		} else {
			d.Alerts.Resolve(ctx, key, "Repository check passed: "+repoName, "")
		}
	}
}

// CheckStale alerts on jobs that have not succeeded for too long.
func (d *Daemon) CheckStale(ctx context.Context) {
	now := time.Now()
	for i := range d.Config.Jobs {
		job := &d.Config.Jobs[i]
		lastSuccess, everSucceeded, err := d.Store.LastSuccessfulBackup(ctx, job.Name)
		if err != nil {
			slog.Error("stale check", "job", job.Name, "error", err)
			continue
		}
		if !everSucceeded {
			lastSuccess = time.Time{}
		}
		stale, limit := isStale(job, lastSuccess, d.started, now)
		key := alert.StaleKey(job.Name)
		if stale {
			lastSuccessText := "never succeeded"
			if everSucceeded {
				lastSuccessText = "last success " + lastSuccess.Format("2006-01-02 15:04")
			}
			d.Alerts.Fire(ctx, key, notify.Warning,
				fmt.Sprintf("Backup %s is overdue", job.Name),
				fmt.Sprintf("No successful backup within %s (%s).", limit, lastSuccessText))
		} else if limit > 0 {
			d.Alerts.Resolve(ctx, key, fmt.Sprintf("Backup %s is up to date", job.Name), "")
		}
	}
}

// isStale reports whether job's last success (zero if never) is older than
// twice the longest gap in its schedule plus its timeout. Jobs that never ran
// are measured from daemon start so a fresh install is not instantly stale.
// Unscheduled jobs are never stale (limit 0).
func isStale(job *config.Job, lastSuccess, started, now time.Time) (stale bool, limit time.Duration) {
	if schedule.IsManual(job.Schedule) {
		return false, 0
	}
	maxGap, ok := schedule.MaxGap(job.Schedule, now)
	if !ok {
		return false, 0
	}
	limit = 2*maxGap + job.Timeout
	measuredFrom := lastSuccess
	if measuredFrom.IsZero() {
		measuredFrom = started
	}
	return now.Sub(measuredFrom) > limit, limit
}

func (d *Daemon) pruneHistory(ctx context.Context) {
	maxAge := d.HistoryMaxAge
	if maxAge == 0 {
		maxAge = 30 * 24 * time.Hour
	}
	if err := d.Store.Prune(ctx, time.Now().Add(-maxAge)); err != nil {
		slog.Error("prune history", "error", err)
	}
}

type cronLogger struct{}

func (cronLogger) Info(msg string, kv ...any) { slog.Debug("cron: "+msg, kv...) }
func (cronLogger) Error(err error, msg string, kv ...any) {
	slog.Error("cron: "+msg, append(kv, "error", err)...)
}

// sweepStaleSecretFiles removes credential files a crashed dump left in the
// temp directory. A file younger than the longest backup timeout may belong to
// a dump running in another sentinel process, so it is kept.
func (d *Daemon) sweepStaleSecretFiles() {
	longestTimeout := time.Hour
	for _, job := range d.Config.Jobs {
		longestTimeout = max(longestTimeout, job.Timeout)
	}
	if removed := engine.SweepStaleSecretFiles(longestTimeout + time.Hour); removed > 0 {
		slog.Warn("removed stale credential files left by a crashed backup", "count", removed)
	}
}
