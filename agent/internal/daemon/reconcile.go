package daemon

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"sentinel/agent/internal/alertkey"
	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/store"
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
	var jobNames []string
	for _, run := range latest {
		if d.configured(run) && !d.reported(run) && !slices.Contains(jobNames, run.Job) {
			jobNames = append(jobNames, run.Job)
		}
	}
	for _, jobName := range jobNames {
		if ctx.Err() != nil {
			return
		}
		d.reconcileJob(ctx, jobName)
	}
}

// reconcileJob reports the latest runs of one job that its alerts do not tell yet. It holds the job's lock
// while it reads them and reports them: RunBackup cannot start the job meanwhile and report something
// newer that these would then overwrite, and a job RunBackup is running now is left to RunBackup.
func (d *Daemon) reconcileJob(ctx context.Context, jobName string) {
	jobLock, _ := d.running.LoadOrStore(jobName, new(sync.Mutex))
	if !jobLock.(*sync.Mutex).TryLock() {
		return
	}
	defer jobLock.(*sync.Mutex).Unlock()
	latest, err := d.Store.LatestBackups(ctx)
	if err != nil {
		slog.Error("reconcile backups", "job", jobName, "error", err)
		return
	}
	var unreported []store.BackupRun
	for _, run := range latest {
		if run.Job == jobName && d.configured(run) && !d.reported(run) {
			unreported = append(unreported, run)
		}
	}
	if len(unreported) > 0 {
		slog.Info("reporting backups run outside the daemon", "job", jobName, "runs", len(unreported))
		d.reportRuns(ctx, jobName, unreported)
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
	failing := d.Alerts.Firing(alertkey.Backup(run.Job, run.Repository))
	if !run.Success {
		return failing
	}
	retentionFailed := run.Error != ""
	return !failing && d.Alerts.Firing(alertkey.Retention(run.Job, run.Repository)) == retentionFailed
}
