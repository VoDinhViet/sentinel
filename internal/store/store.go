// Package store persists backup runs, check results and alert state
// in SQLite so the CLI and a future dashboard can read history.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

var migrations = []string{
	`CREATE TABLE backup_runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		job TEXT NOT NULL,
		repository TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		finished_at INTEGER NOT NULL,
		success INTEGER NOT NULL,
		snapshot_id TEXT NOT NULL DEFAULT '',
		files_new INTEGER NOT NULL DEFAULT 0,
		files_changed INTEGER NOT NULL DEFAULT 0,
		bytes_added INTEGER NOT NULL DEFAULT 0,
		total_bytes INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX backup_runs_job ON backup_runs(job, success, finished_at);
	CREATE TABLE check_results (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		at INTEGER NOT NULL,
		ok INTEGER NOT NULL,
		latency_ms INTEGER NOT NULL,
		error TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX check_results_name ON check_results(name, at);
	CREATE TABLE metrics (
		at INTEGER NOT NULL,
		cpu REAL NOT NULL,
		memory REAL NOT NULL,
		swap REAL NOT NULL,
		load1 REAL NOT NULL,
		disk_max REAL NOT NULL
	);
	CREATE INDEX metrics_at ON metrics(at);
	CREATE TABLE alerts (
		key TEXT PRIMARY KEY,
		firing INTEGER NOT NULL,
		since INTEGER NOT NULL,
		last_notified INTEGER NOT NULL,
		message TEXT NOT NULL
	);`,
	// Resource metrics are no longer collected.
	`DROP TABLE metrics;`,
	// Alert keys renamed: repo-check:<repo> -> verify:<repo>, backup-stale:<job> -> stale:<job>.
	// Rewriting them keeps an alert that is open now from being announced a second time.
	`UPDATE alerts SET key = 'verify:' || substr(key, 12) WHERE substr(key, 1, 11) = 'repo-check:';
	UPDATE alerts SET key = 'stale:' || substr(key, 14) WHERE substr(key, 1, 13) = 'backup-stale:';`,
}

// walSuffixes are the files SQLite keeps next to a database in WAL mode.
var walSuffixes = []string{"-wal", "-shm"}

// Open opens (creating if needed) the database at path and applies migrations.
// The database holds restic and dump error text, so it and its WAL files are
// readable by their owner only, whatever the umask and whatever mode an older
// version left on an existing file.
func Open(path string) (*Store, error) {
	onDisk := path != ":memory:"
	if onDisk {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
		// SQLite gives the -wal and -shm files the mode of the database file.
		if err := createPrivate(path); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; one connection avoids SQLITE_BUSY between
	// our own goroutines and keeps :memory: databases shared.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close() // the migration error is the one to report
		return nil, err
	}
	if onDisk {
		// A WAL file left by an older version keeps its own mode.
		for _, suffix := range walSuffixes {
			if err := os.Chmod(path+suffix, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
				if !modeUnsupported(err) {
					_ = db.Close()
					return nil, err
				}
				warnModeUnsupported(path+suffix, err)
			}
		}
	}
	return s, nil
}

// createPrivate creates the file when it is missing and sets its mode to 0600.
func createPrivate(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		if !modeUnsupported(err) {
			_ = file.Close()
			return err
		}
		warnModeUnsupported(path, err)
	}
	return file.Close()
}

// modeUnsupported reports a chmod error that means this file system cannot hold Unix modes (vfat, some
// network and Windows-backed mounts) or that the file is another user's, as opposed to a real failure.
func modeUnsupported(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP)
}

// warnModeUnsupported says the state database could not be restricted to its owner. The daemon still
// starts: refusing would stop every backup over a mount the operator may have chosen on purpose, and the
// state directory (0700 where it can be) is still the barrier.
func warnModeUnsupported(path string, err error) {
	slog.Warn("cannot restrict the state database to its owner: the file system does not allow changing modes", "path", path, "error", err)
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

type BackupRun struct {
	ID           int64 // set when read back; ignored by RecordBackup
	Job          string
	Repository   string
	StartedAt    time.Time
	FinishedAt   time.Time
	Success      bool
	SnapshotID   string
	FilesNew     int64
	FilesChanged int64
	BytesAdded   int64
	TotalBytes   int64
	// Error says why a run failed. On a successful run it is a warning: the
	// snapshot exists but a later step (retention) failed.
	Error string
}

// Outcome is "failed", "warning" (a snapshot exists but retention failed) or "ok".
func (r BackupRun) Outcome() string {
	switch {
	case !r.Success:
		return "failed"
	case r.Error != "":
		return "warning"
	}
	return "ok"
}

// Summary is how the run reads in the CLI: "ok", "ok (warning: ...)" when the
// snapshot was made but retention failed, or "FAILED: ...".
func (r BackupRun) Summary() string {
	switch {
	case !r.Success:
		return "FAILED: " + r.Error
	case r.Error != "":
		return "ok (warning: " + r.Error + ")"
	}
	return "ok"
}

func (s *Store) RecordBackup(ctx context.Context, run BackupRun) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_runs
		(job, repository, started_at, finished_at, success, snapshot_id, files_new, files_changed, bytes_added, total_bytes, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.Job, run.Repository, run.StartedAt.Unix(), run.FinishedAt.Unix(), run.Success, run.SnapshotID,
		run.FilesNew, run.FilesChanged, run.BytesAdded, run.TotalBytes, run.Error)
	return err
}

// LastSuccessfulBackup returns when job last finished successfully on any
// repository. ok is false if it never has.
func (s *Store) LastSuccessfulBackup(ctx context.Context, job string) (finishedAt time.Time, ok bool, err error) {
	var finishedUnix sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`SELECT MAX(finished_at) FROM backup_runs WHERE job = ? AND success = 1`, job).Scan(&finishedUnix)
	if err != nil || !finishedUnix.Valid {
		return time.Time{}, false, err
	}
	return time.Unix(finishedUnix.Int64, 0), true, nil
}

// LastBackupSize is how much data the latest successful backup of the job into the repository
// read (restic's total_bytes_processed; for a database, the size of the dump): the best guide
// to how big the next one will be. ok is false when there is no such backup.
func (s *Store) LastBackupSize(ctx context.Context, job, repository string) (size int64, ok bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT total_bytes FROM backup_runs WHERE job = ? AND repository = ? AND success = 1 AND total_bytes > 0
		 ORDER BY finished_at DESC, id DESC LIMIT 1`, job, repository).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return size, err == nil, err
}

const backupCols = `id, job, repository, started_at, finished_at, success,
	snapshot_id, files_new, files_changed, bytes_added, total_bytes, error`

func (s *Store) queryBackups(ctx context.Context, query string, args ...any) ([]BackupRun, error) {
	runRows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer runRows.Close()
	var runs []BackupRun
	for runRows.Next() {
		var run BackupRun
		var startedUnix, finishedUnix int64
		if err := runRows.Scan(&run.ID, &run.Job, &run.Repository, &startedUnix, &finishedUnix, &run.Success, &run.SnapshotID,
			&run.FilesNew, &run.FilesChanged, &run.BytesAdded, &run.TotalBytes, &run.Error); err != nil {
			return nil, err
		}
		run.StartedAt, run.FinishedAt = time.Unix(startedUnix, 0), time.Unix(finishedUnix, 0)
		runs = append(runs, run)
	}
	return runs, runRows.Err()
}

// LatestBackups returns the most recent run for each (job, repository) pair.
func (s *Store) LatestBackups(ctx context.Context) ([]BackupRun, error) {
	return s.queryBackups(ctx, `SELECT `+backupCols+` FROM backup_runs b WHERE id = (
			SELECT MAX(id) FROM backup_runs WHERE job = b.job AND repository = b.repository)
		ORDER BY job, repository`)
}

// RecentBackups returns up to perPair of the latest runs of each (job, repository)
// pair, ordered by job and repository, and within a pair oldest first.
func (s *Store) RecentBackups(ctx context.Context, perPair int) ([]BackupRun, error) {
	return s.queryBackups(ctx, `SELECT `+backupCols+` FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY job, repository ORDER BY id DESC) AS recency
			FROM backup_runs) WHERE recency <= ? ORDER BY job, repository, id`, perPair)
}

// BackupRunsSince returns up to limit runs with id > afterID, oldest first.
func (s *Store) BackupRunsSince(ctx context.Context, afterID int64, limit int) ([]BackupRun, error) {
	return s.queryBackups(ctx, `SELECT `+backupCols+` FROM backup_runs WHERE id > ? ORDER BY id LIMIT ?`, afterID, limit)
}

type CheckResult struct {
	ID      int64
	Name    string
	At      time.Time
	OK      bool
	Latency time.Duration
	Error   string
}

func (s *Store) RecordCheck(ctx context.Context, check CheckResult) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO check_results (name, at, ok, latency_ms, error) VALUES (?, ?, ?, ?, ?)`,
		check.Name, check.At.Unix(), check.OK, check.Latency.Milliseconds(), check.Error)
	return err
}

func (s *Store) queryChecks(ctx context.Context, query string, args ...any) ([]CheckResult, error) {
	checkRows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer checkRows.Close()
	var checks []CheckResult
	for checkRows.Next() {
		var check CheckResult
		var atUnix, latencyMS int64
		if err := checkRows.Scan(&check.ID, &check.Name, &atUnix, &check.OK, &latencyMS, &check.Error); err != nil {
			return nil, err
		}
		check.At, check.Latency = time.Unix(atUnix, 0), time.Duration(latencyMS)*time.Millisecond
		checks = append(checks, check)
	}
	return checks, checkRows.Err()
}

// LatestChecks returns the most recent result for each check name.
func (s *Store) LatestChecks(ctx context.Context) ([]CheckResult, error) {
	return s.queryChecks(ctx, `SELECT id, name, at, ok, latency_ms, error FROM check_results c
		WHERE id = (SELECT MAX(id) FROM check_results WHERE name = c.name) ORDER BY name`)
}

// RecentChecks returns up to perName of the latest results of each check, ordered by name
// and, within a check, oldest first.
func (s *Store) RecentChecks(ctx context.Context, perName int) ([]CheckResult, error) {
	return s.queryChecks(ctx, `SELECT id, name, at, ok, latency_ms, error FROM (
			SELECT *, ROW_NUMBER() OVER (PARTITION BY name ORDER BY id DESC) AS recency
			FROM check_results) WHERE recency <= ? ORDER BY name, id`, perName)
}

// ChecksSince returns up to limit results with id > afterID, oldest first.
func (s *Store) ChecksSince(ctx context.Context, afterID int64, limit int) ([]CheckResult, error) {
	return s.queryChecks(ctx, `SELECT id, name, at, ok, latency_ms, error FROM check_results
		WHERE id > ? ORDER BY id LIMIT ?`, afterID, limit)
}

type Alert struct {
	Key          string
	Firing       bool
	Since        time.Time
	LastNotified time.Time
	Message      string
}

func (s *Store) SaveAlert(ctx context.Context, alert Alert) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO alerts (key, firing, since, last_notified, message)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET firing = excluded.firing, since = excluded.since,
			last_notified = excluded.last_notified, message = excluded.message`,
		alert.Key, alert.Firing, alert.Since.Unix(), alert.LastNotified.Unix(), alert.Message)
	return err
}

// Alerts returns every stored alert; firingOnly limits it to open alerts.
func (s *Store) Alerts(ctx context.Context, firingOnly bool) ([]Alert, error) {
	query := `SELECT key, firing, since, last_notified, message FROM alerts`
	if firingOnly {
		query += ` WHERE firing = 1`
	}
	alertRows, err := s.db.QueryContext(ctx, query+` ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer alertRows.Close()
	var alerts []Alert
	for alertRows.Next() {
		var alert Alert
		var sinceUnix, lastNotifiedUnix int64
		if err := alertRows.Scan(&alert.Key, &alert.Firing, &sinceUnix, &lastNotifiedUnix, &alert.Message); err != nil {
			return nil, err
		}
		alert.Since, alert.LastNotified = time.Unix(sinceUnix, 0), time.Unix(lastNotifiedUnix, 0)
		alerts = append(alerts, alert)
	}
	return alerts, alertRows.Err()
}

// Prune deletes check results older than before.
// Backup runs are small and kept forever.
func (s *Store) Prune(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM check_results WHERE at < ?`, before.Unix())
	return err
}
