package backup

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"sentinel/internal/config"
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
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// JobTags are the restic tags that identify a job's snapshots.
func JobTags(job *config.Backup) []string {
	return []string{"job=" + job.Name}
}

// Restic returns a client for one of the configured repositories.
func (r *Runner) Restic(repoName string) (*Restic, error) {
	repo, ok := r.Config.Repositories[repoName]
	if !ok {
		return nil, fmt.Errorf("unknown repository %q", repoName)
	}
	return &Restic{Bin: r.Bin, Repo: repo, Host: r.Config.ServerName}, nil
}

// Run backs the job up to each of its repositories in turn and applies
// retention. Every repository is attempted even if an earlier one fails;
// the returned runs say which succeeded.
func (r *Runner) Run(ctx context.Context, job *config.Backup) []store.BackupRun {
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
	}
	return runs
}

func (r *Runner) runOne(ctx context.Context, job *config.Backup, repoName string) store.BackupRun {
	run := store.BackupRun{Job: job.Name, Repository: repoName, StartedAt: r.now()}
	log := slog.With("job", job.Name, "repository", repoName)
	log.Info("backup started")

	fail := func(err error) store.BackupRun {
		run.FinishedAt, run.Error = r.now(), err.Error()
		log.Error("backup failed", "error", err)
		return run
	}

	rc, err := r.Restic(repoName)
	if err != nil {
		return fail(err)
	}
	opts := BackupOptions{Tags: JobTags(job)}
	switch job.Type {
	case config.BackupFiles:
		opts.Paths, opts.Excludes = job.Paths, job.Exclude
	case config.BackupPostgres:
		opts.Command, opts.StdinFilename = PgDumpCommand(job.Postgres)
		if job.Postgres.Password != "" {
			rc.ExtraEnv = append(rc.ExtraEnv, "PGPASSWORD="+job.Postgres.Password)
		}
	default:
		return fail(fmt.Errorf("unsupported backup type %q", job.Type))
	}

	sum, err := rc.Backup(ctx, opts)
	for try := 0; IsLocked(err) && try < r.LockRetries; try++ {
		log.Warn("repository locked, retrying", "in", r.LockRetryDelay)
		select {
		case <-ctx.Done():
			return fail(fmt.Errorf("%w (gave up waiting for lock: %v)", err, ctx.Err()))
		case <-time.After(r.LockRetryDelay):
		}
		sum, err = rc.Backup(ctx, opts)
	}
	run.SnapshotID, run.FilesNew, run.FilesChanged = sum.SnapshotID, sum.FilesNew, sum.FilesChanged
	run.BytesAdded, run.TotalBytes = sum.BytesAdded, sum.TotalBytes
	if err != nil {
		return fail(err)
	}

	if err := rc.Forget(ctx, JobTags(job), job.Retention); err != nil {
		// The new snapshot is safe; retention can catch up next run.
		return fail(fmt.Errorf("backup ok (snapshot %.8s) but retention failed: %w", sum.SnapshotID, err))
	}

	run.FinishedAt, run.Success = r.now(), true
	log.Info("backup finished", "snapshot", sum.SnapshotID[:min(8, len(sum.SnapshotID))],
		"bytes_added", sum.BytesAdded, "duration", run.FinishedAt.Sub(run.StartedAt).Round(time.Second))
	return run
}

// PgDumpCommand returns the pg_dump invocation for a job, in custom format
// (compressed, restorable with pg_restore), and the file name it is stored as.
func PgDumpCommand(pg *config.Postgres) (cmd []string, filename string) {
	return []string{
		"pg_dump", "--format=custom", "--no-password",
		"--host", pg.Host, "--port", strconv.Itoa(pg.Port),
		"--username", pg.User, "--dbname", pg.Database,
	}, pg.Database + ".dump"
}
