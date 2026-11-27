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
	"runtime/debug"
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

// historyMaxAge is how long uptime check results are kept.
const historyMaxAge = 30 * 24 * time.Hour

// retryAlertsEvery is how often alert messages that no notifier took are sent again.
const retryAlertsEvery = 5 * time.Minute

// reconcileEvery is how often the backups run outside the daemon are looked for (see ReconcileBackups).
const reconcileEvery = time.Minute

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

	started time.Time
	// running holds one mutex per job name, so a job never runs twice at once
	// (a "backup now" overlapping a scheduled run would fight over the repository
	// lock and dump the same database twice).
	running sync.Map // job name -> *sync.Mutex
	// gates keeps a prune of a repository apart from the backups into it.
	gates sync.Map // repository name -> *repoGate
}

// runAtStart is the first run of a task. A panic in it is logged, as the scheduler does for the later
// runs, and does not end the daemon.
func (d *Daemon) runAtStart(ctx context.Context, task Task) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("task panicked", "task", task.Name, "panic", recovered, "stack", string(debug.Stack()))
		}
	}()
	task.Run(ctx)
}

func (d *Daemon) gate(repoName string) *repoGate {
	gate, _ := d.gates.LoadOrStore(repoName, new(repoGate))
	return gate.(*repoGate)
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

	if !schedule.IsManual(d.Config.PruneSchedule) {
		cronSchedule, err := schedule.Parse(d.Config.PruneSchedule)
		if err != nil {
			return fmt.Errorf("schedule repo prune: %w", err)
		}
		cronRunner.Schedule(cronSchedule, cron.FuncJob(func() { d.PruneRepositories(jobCtx) }))
	}

	tasks := append(d.builtinTasks(), d.Tasks...)
	for _, task := range tasks {
		cronRunner.Schedule(cron.Every(task.Every), cron.FuncJob(func() { task.Run(jobCtx) }))
		slog.Debug("scheduled task", "task", task.Name, "every", task.Every)
	}

	var startup sync.WaitGroup
	for _, task := range tasks {
		if task.AtStart {
			startup.Go(func() { d.runAtStart(jobCtx, task) })
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

	// The repositories are entered in name order, and a prune holds one at a time, so they cannot wait for each other.
	for _, repoName := range slices.Sorted(slices.Values(job.Repositories)) {
		if err := d.gate(repoName).enter(ctx); err != nil {
			return []store.BackupRun{{Job: job.Name, Repository: repoName, Error: backup.Stopped}} // shutting down, still waiting for a prune
		}
		defer d.gate(repoName).leave()
	}

	runs := d.Runner.Run(ctx, job)
	if len(runs) == 1 && runs[0].Error == backup.AlreadyRunning {
		return runs // another sentinel process holds the job: nothing ran, nothing to alert
	}
	if ctx.Err() != nil {
		// Shutting down: the run was interrupted, not broken.
		return runs
	}
	d.reportRuns(ctx, job.Name, runs)
	return runs
}

// reportRuns raises or resolves the alerts of the runs of one job, one per repository, and sends the
// success message when the config asks for it. A stopped run says nothing: somebody did that on purpose.
func (d *Daemon) reportRuns(ctx context.Context, jobName string, runs []store.BackupRun) {
	var successLines []string
	for _, run := range runs {
		key := alert.BackupKey(jobName, run.Repository)
		if run.Error == backup.Stopped {
			continue // somebody stopped it on purpose: neither a failure nor a success
		}
		if run.Success {
			d.Alerts.Resolve(ctx, key, fmt.Sprintf("Backup %s to %s succeeded", jobName, run.Repository), "")
			successLines = append(successLines, fmt.Sprintf("%s: snapshot %.8s, +%d bytes", run.Repository, run.SnapshotID, run.BytesAdded))
			// The snapshot is safe, but retention failed: warn separately from a failed backup.
			retentionKey := alert.RetentionKey(jobName, run.Repository)
			if run.Error != "" {
				d.Alerts.Fire(ctx, retentionKey, notify.Warning,
					fmt.Sprintf("Retention for %s in %s failed", jobName, run.Repository), run.Error)
			} else {
				d.Alerts.Resolve(ctx, retentionKey, fmt.Sprintf("Retention for %s in %s succeeded", jobName, run.Repository), "")
			}
			continue
		}
		d.Alerts.Fire(ctx, key, notify.Critical,
			fmt.Sprintf("Backup %s to %s failed", jobName, run.Repository), run.Error)
	}
	if d.Config.Alert.NotifySuccess && len(successLines) > 0 {
		d.Alerts.Info(ctx, "Backup "+jobName+" completed", strings.Join(successLines, "\n"))
	}
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

// usedRepositories are the repositories some job backs up to, by name.
func (d *Daemon) usedRepositories() []string {
	usedRepos := map[string]bool{}
	for _, job := range d.Config.Jobs {
		for _, repoName := range job.Repositories {
			usedRepos[repoName] = true
		}
	}
	return slices.Sorted(maps.Keys(usedRepos))
}

// prunedRepositories are the repositories a job with a retention backs up to: the others forget nothing,
// so a prune there would only take the lock to find nothing to remove.
func (d *Daemon) prunedRepositories() []string {
	repoNames := map[string]bool{}
	for _, job := range d.Config.Jobs {
		if job.Retention.Empty() {
			continue
		}
		for _, repoName := range job.Repositories {
			repoNames[repoName] = true
		}
	}
	return slices.Sorted(maps.Keys(repoNames))
}

// PruneRepositories removes the data of the snapshots that retention forgot, once per repository. A job
// only forgets (see backup.Runner.DeferPrune): prune reads the whole repository and holds its lock, which
// is too much to do after every backup.
func (d *Daemon) PruneRepositories(ctx context.Context) {
	for _, repoName := range d.prunedRepositories() {
		// The backups into the repository end first and the ones that come meanwhile wait: see repoGate.
		// The wait is not part of the prune's own timeout.
		if d.gate(repoName).exclusive(ctx) != nil {
			return
		}
		resticClient, err := d.Runner.Restic(repoName)
		if err == nil {
			pruneCtx, cancel := context.WithTimeout(ctx, d.verifyTimeout())
			err = resticClient.Prune(pruneCtx)
			cancel()
		}
		d.gate(repoName).release()
		if ctx.Err() != nil {
			return
		}
		key := alert.PruneKey(repoName)
		if err != nil {
			d.Alerts.Fire(ctx, key, notify.Warning, "Repository prune failed: "+repoName, err.Error())
		} else {
			d.Alerts.Resolve(ctx, key, "Repository prune succeeded: "+repoName, "")
		}
	}
}

// CheckRepositories runs `restic check` on every repository used by a job.
func (d *Daemon) CheckRepositories(ctx context.Context) {
	for _, repoName := range d.usedRepositories() {
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

// isStale reports whether job has missed two scheduled runs in a row since its last success (zero if
// never), and gave the second one its timeout to finish. limit is how long after the last success that
// point lies. Jobs that never ran are measured from daemon start so a fresh install is not instantly
// stale. Unscheduled jobs are never stale (limit 0).
func isStale(job *config.Job, lastSuccess, started, now time.Time) (stale bool, limit time.Duration) {
	if schedule.IsManual(job.Schedule) {
		return false, 0
	}
	measuredFrom := lastSuccess
	if measuredFrom.IsZero() {
		measuredFrom = started
	}
	secondRun, ok := schedule.SecondRunAfter(job.Schedule, measuredFrom)
	if !ok {
		return false, 0
	}
	deadline := secondRun.Add(job.Timeout)
	return now.After(deadline), deadline.Sub(measuredFrom)
}

// builtinTasks are the periodic jobs every daemon runs besides the backups. The prune also runs at start:
// every change of the config asks for a restart, so a daemon restarted more often than daily would
// never reach its first 24 hours.
func (d *Daemon) builtinTasks() []Task {
	return []Task{
		{Name: "reconcile-backups", Every: reconcileEvery, Run: d.ReconcileBackups, AtStart: true},
		{Name: "stale-backups", Every: 15 * time.Minute, Run: d.CheckStale},
		{Name: "retry-alerts", Every: retryAlertsEvery, Run: d.Alerts.RetryPending},
		{Name: "prune-history", Every: 24 * time.Hour, Run: d.pruneHistory, AtStart: true},
	}
}

func (d *Daemon) pruneHistory(ctx context.Context) {
	if err := d.Store.Prune(ctx, time.Now().Add(-historyMaxAge)); err != nil {
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
