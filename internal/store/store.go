// Package store persists backup runs, check results, metrics and alert state
// in SQLite so the CLI and a future dashboard can read history.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
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
		db.Close()
		return nil, err
	}
	return s, nil
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
	Error        string
}

func (s *Store) RecordBackup(ctx context.Context, r BackupRun) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_runs
		(job, repository, started_at, finished_at, success, snapshot_id, files_new, files_changed, bytes_added, total_bytes, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Job, r.Repository, r.StartedAt.Unix(), r.FinishedAt.Unix(), r.Success, r.SnapshotID,
		r.FilesNew, r.FilesChanged, r.BytesAdded, r.TotalBytes, r.Error)
	return err
}

// LastSuccessfulBackup returns when job last finished successfully on any
// repository. ok is false if it never has.
func (s *Store) LastSuccessfulBackup(ctx context.Context, job string) (t time.Time, ok bool, err error) {
	var unix sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`SELECT MAX(finished_at) FROM backup_runs WHERE job = ? AND success = 1`, job).Scan(&unix)
	if err != nil || !unix.Valid {
		return time.Time{}, false, err
	}
	return time.Unix(unix.Int64, 0), true, nil
}

// LatestBackups returns the most recent run for each (job, repository) pair.
func (s *Store) LatestBackups(ctx context.Context) ([]BackupRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT job, repository, started_at, finished_at, success,
		snapshot_id, files_new, files_changed, bytes_added, total_bytes, error
		FROM backup_runs b WHERE id = (
			SELECT MAX(id) FROM backup_runs WHERE job = b.job AND repository = b.repository)
		ORDER BY job, repository`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupRun
	for rows.Next() {
		var r BackupRun
		var started, finished int64
		if err := rows.Scan(&r.Job, &r.Repository, &started, &finished, &r.Success, &r.SnapshotID,
			&r.FilesNew, &r.FilesChanged, &r.BytesAdded, &r.TotalBytes, &r.Error); err != nil {
			return nil, err
		}
		r.StartedAt, r.FinishedAt = time.Unix(started, 0), time.Unix(finished, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

type CheckResult struct {
	Name    string
	At      time.Time
	OK      bool
	Latency time.Duration
	Error   string
}

func (s *Store) RecordCheck(ctx context.Context, r CheckResult) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO check_results (name, at, ok, latency_ms, error) VALUES (?, ?, ?, ?, ?)`,
		r.Name, r.At.Unix(), r.OK, r.Latency.Milliseconds(), r.Error)
	return err
}

// LatestChecks returns the most recent result for each check name.
func (s *Store) LatestChecks(ctx context.Context) ([]CheckResult, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, at, ok, latency_ms, error FROM check_results c
		WHERE id = (SELECT MAX(id) FROM check_results WHERE name = c.name) ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckResult
	for rows.Next() {
		var r CheckResult
		var at, ms int64
		if err := rows.Scan(&r.Name, &at, &r.OK, &ms, &r.Error); err != nil {
			return nil, err
		}
		r.At, r.Latency = time.Unix(at, 0), time.Duration(ms)*time.Millisecond
		out = append(out, r)
	}
	return out, rows.Err()
}

type Metrics struct {
	At      time.Time
	CPU     float64
	Memory  float64
	Swap    float64
	Load1   float64
	DiskMax float64
}

func (s *Store) RecordMetrics(ctx context.Context, m Metrics) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO metrics (at, cpu, memory, swap, load1, disk_max) VALUES (?, ?, ?, ?, ?, ?)`,
		m.At.Unix(), m.CPU, m.Memory, m.Swap, m.Load1, m.DiskMax)
	return err
}

// LatestMetrics returns the newest sample; ok is false when none exist.
func (s *Store) LatestMetrics(ctx context.Context) (m Metrics, ok bool, err error) {
	var at int64
	err = s.db.QueryRowContext(ctx, `SELECT at, cpu, memory, swap, load1, disk_max FROM metrics
		ORDER BY at DESC LIMIT 1`).Scan(&at, &m.CPU, &m.Memory, &m.Swap, &m.Load1, &m.DiskMax)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	m.At = time.Unix(at, 0)
	return m, true, nil
}

type Alert struct {
	Key          string
	Firing       bool
	Since        time.Time
	LastNotified time.Time
	Message      string
}

func (s *Store) SaveAlert(ctx context.Context, a Alert) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO alerts (key, firing, since, last_notified, message)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET firing = excluded.firing, since = excluded.since,
			last_notified = excluded.last_notified, message = excluded.message`,
		a.Key, a.Firing, a.Since.Unix(), a.LastNotified.Unix(), a.Message)
	return err
}

// Alerts returns every stored alert; firingOnly limits it to open alerts.
func (s *Store) Alerts(ctx context.Context, firingOnly bool) ([]Alert, error) {
	q := `SELECT key, firing, since, last_notified, message FROM alerts`
	if firingOnly {
		q += ` WHERE firing = 1`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var since, last int64
		if err := rows.Scan(&a.Key, &a.Firing, &since, &last, &a.Message); err != nil {
			return nil, err
		}
		a.Since, a.LastNotified = time.Unix(since, 0), time.Unix(last, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// Prune deletes check results and metric samples older than before.
// Backup runs are small and kept forever.
func (s *Store) Prune(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM check_results WHERE at < ?`, before.Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM metrics WHERE at < ?`, before.Unix())
	return err
}
