package daemon

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"sentinel/internal/alert"
	"sentinel/internal/backup"
	"sentinel/internal/store"
)

// ReconcileBackups raises and resolves the alerts of backups that did not run in this process: a backup
// started by hand (`sentinel backup run`, the manager, the API) ends in another process, which records its
// run but has no alert state of its own. The daemon owns the alerts, so it looks at the latest run of every
// job and repository and reports the ones whose alert does not match, the same way RunBackup does for its own.
// A run that is already reported is left alone, so nothing is announced twice and no reminder is started.
func (d *Daemon) ReconcileBackups(ctx context.Context) {
	latest, err := d.Store.LatestBackups(ctx)
	if err != nil {
		slog.Error("reconcile backups", "error", err)
		return
	}
	unreported := map[string][]store.BackupRun{}
	for _, run := range latest {
		if d.configured(run) && !d.reported(run) && !d.jobRunning(run.Job) {
			unreported[run.Job] = append(unreported[run.Job], run)
		}
	}
	for jobName, runs := range unreported {
		if ctx.Err() != nil {
			return
		}
		slog.Info("reporting backups run outside the daemon", "job", jobName, "runs", len(runs))
		d.reportRuns(ctx, jobName, runs)
	}
}

// configured says the run belongs to a job and repository that the config still has: the alert of a job
// that was removed would never be resolved.
func (d *Daemon) configured(run store.BackupRun) bool {
	for _, job := range d.Config.Jobs {
		if job.Name == run.Job {
			return slices.Contains(job.Repositories, run.Repository)
		}
	}
	return false
}

// reported says the alerts of the run already tell its outcome.
func (d *Daemon) reported(run store.BackupRun) bool {
	if run.Error == backup.Stopped {
		return true // nothing to say about a backup somebody stopped
	}
	failing := d.Alerts.Firing(alert.BackupKey(run.Job, run.Repository))
	if !run.Success {
		return failing
	}
	retentionFailed := run.Error != ""
	return !failing && d.Alerts.Firing(alert.RetentionKey(run.Job, run.Repository)) == retentionFailed
}

// jobRunning says RunBackup is running the job now: it reports the outcome itself when it ends.
func (d *Daemon) jobRunning(jobName string) bool {
	jobLock, _ := d.running.LoadOrStore(jobName, new(sync.Mutex))
	if jobLock.(*sync.Mutex).TryLock() {
		jobLock.(*sync.Mutex).Unlock()
		return false
	}
	return true
}
