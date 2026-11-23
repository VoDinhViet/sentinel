// Package backup runs configured backup jobs: dump, restic backup, retention, job
// locking and recording the outcome of each run.
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/engine"
	"sentinel/internal/restic"
	"sentinel/internal/store"
)

// Runner executes configured backup jobs and records the outcome.
type Runner struct {
	Config *config.Config
	Store  *store.Store // optional
	Bin    string       // restic binary, default "restic"
	Now    func() time.Time
	// LockRetries is how many times a backup is retried when the repository
	// is locked by another restic process; LockRetryDelay is the wait between.
	LockRetries    int
	LockRetryDelay time.Duration
	// OnProgress, when set, hears how far a running backup has got, about every two seconds
	// (from another goroutine). The same progress is written to a file next to the state
	// database for other processes to read: see ReadRunning.
	OnProgress func(Progress)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// JobTags are the restic tags that identify a job's snapshots.
func JobTags(job *config.Job) []string {
	return []string{"job=" + job.Name}
}

// Restic returns a client for one of the configured repositories.
func (r *Runner) Restic(repoName string) (*restic.Client, error) {
	repo, ok := r.Config.Repositories[repoName]
	if !ok {
		return nil, fmt.Errorf("unknown repository %q", repoName)
	}
	return &restic.Client{Bin: r.Bin, Repo: repo, Host: r.Config.ServerName}, nil
}

// backupClient is the client a backup uses: restic is the same, but it runs at the priority
// and upload speed the config asks for, so a large backup does not starve the server.
func (r *Runner) backupClient(repoName string) (*restic.Client, error) {
	client, err := r.Restic(repoName)
	if err != nil {
		return nil, err
	}
	client.LowPriority = r.Config.Resources.Priority != config.PriorityNormal
	client.UploadLimitKiB = r.Config.Resources.UploadLimitKiB
	return client, nil
}

// Run backs the job up to each of its repositories in turn and applies
// retention. Every repository is attempted even if an earlier one fails;
// the returned runs say which succeeded.
func (r *Runner) Run(ctx context.Context, job *config.Job) []store.BackupRun {
	held, release, err := r.lockJob(job.Name)
	defer release()
	if err != nil {
		slog.Error("lock job", "job", job.Name, "error", err)
		return []store.BackupRun{{Job: job.Name, StartedAt: r.now(), FinishedAt: r.now(), Error: err.Error()}}
	}
	if !held {
		slog.Warn("backup skipped, already running", "job", job.Name)
		return []store.BackupRun{{Job: job.Name, Error: AlreadyRunning}}
	}
	// Wait for a slot before the clock of the job's timeout starts: waiting is not running.
	releaseSlot, err := r.acquireSlot(ctx, job.Name)
	defer releaseSlot()
	if err != nil {
		return []store.BackupRun{{Job: job.Name, StartedAt: r.now(), FinishedAt: r.now(), Error: err.Error()}}
	}
	ctx, cancel := context.WithTimeout(ctx, job.Timeout)
	defer cancel()

	var runs []store.BackupRun
	for _, repoName := range job.Repositories {
		run := r.runOne(ctx, job, repoName)
		if r.Store != nil {
			// Record with a fresh context: a timed-out backup must still be logged.
			if err := r.Store.RecordBackup(context.WithoutCancel(ctx), run); err != nil {
				slog.Error("record backup run", "job", job.Name, "error", err)
			}
		}
		runs = append(runs, run)
		if run.Error == Stopped {
			break // stopping a job stops it for the repositories that are left too
		}
	}
	return runs
}

func (r *Runner) runOne(ctx context.Context, job *config.Job, repoName string) store.BackupRun {
	run := store.BackupRun{Job: job.Name, Repository: repoName, StartedAt: r.now()}
	logger := slog.With("job", job.Name, "repository", repoName)
	logger.Info("backup started")

	fail := func(err error) store.BackupRun {
		run.FinishedAt, run.Error = r.now(), err.Error()
		logger.Error("backup failed", "error", err)
		return run
	}

	client, err := r.backupClient(repoName)
	if err != nil {
		return fail(err)
	}
	opts := restic.BackupOptions{Tags: JobTags(job)}
	if job.Type == config.JobFiles {
		opts.Paths, opts.Excludes = job.Paths, job.Excludes
	} else {
		dbEngine, ok := engine.For(job.Type)
		if !ok {
			return fail(fmt.Errorf("unsupported backup type %q", job.Type))
		}
		dump, err := dbEngine.Dump(job)
		if err != nil {
			return fail(err)
		}
		if dump.Cleanup != nil {
			defer dump.Cleanup()
		}
		opts.Command, opts.StdinFilename = dump.Command, dump.Filename
		client.ExtraEnv = append(client.ExtraEnv, dump.Env...)
	}

	var estimate int64
	if r.Store != nil && job.Type != config.JobFiles {
		// A dump's size is not known beforehand: the last one is the best guess.
		estimate, _, _ = r.Store.LastBackupSize(ctx, job.Name, repoName)
	}
	progressDir := ""
	if r.Config != nil && r.Config.StorePath != "" {
		progressDir = ProgressDir(r.Config.StorePath)
	}
	ctx, stop := context.WithCancel(ctx) // the user may stop this backup: see RequestStop
	defer stop()
	progress := newTracker(progressDir, job.Name, repoName, estimate, job.Timeout, r.now, r.OnProgress, stop)
	defer progress.finish()
	opts.OnStatus = progress.status

	sum, err := client.Backup(ctx, opts)
	if err != nil && progress.stopped.Load() {
		return fail(errors.New(Stopped))
	}
	if restic.IsNoRepo(err) {
		// The first backup into a folder of this machine creates the repository there.
		created, initErr := initLocalRepo(ctx, client)
		if initErr != nil {
			return fail(initErr)
		}
		if created {
			logger.Info("repository created", "url", client.Repo.URL)
			sum, err = client.Backup(ctx, opts)
		}
	}
	for attempt := 0; restic.IsLocked(err) && attempt < r.LockRetries; attempt++ {
		logger.Warn("repository locked, retrying", "in", r.LockRetryDelay)
		select {
		case <-ctx.Done():
			return fail(fmt.Errorf("%w (gave up waiting for lock: %w)", err, ctx.Err()))
		case <-time.After(r.LockRetryDelay):
		}
		sum, err = client.Backup(ctx, opts)
	}
	run.SnapshotID, run.FilesNew, run.FilesChanged = sum.SnapshotID, sum.FilesNew, sum.FilesChanged
	run.BytesAdded, run.TotalBytes = sum.BytesAdded, sum.TotalBytes
	if err != nil {
		return fail(err)
	}

	run.Success = true
	progress.phase(PhaseRetention)
	if err := client.Forget(ctx, JobTags(job), job.Retention); err != nil {
		// The new snapshot is safe, so the backup counts as successful; Error
		// carries the retention failure and retention catches up next run.
		run.Error = fmt.Sprintf("retention failed: %v", err)
		logger.Error("retention failed", "snapshot", sum.SnapshotID, "error", err)
	}
	run.FinishedAt = r.now()
	logger.Info("backup finished", "snapshot", sum.SnapshotID[:min(8, len(sum.SnapshotID))],
		"bytes_added", sum.BytesAdded, "duration", run.FinishedAt.Sub(run.StartedAt).Round(time.Second))
	return run
}
