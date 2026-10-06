package service

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var migrations = []string{
	`CREATE TABLE agents (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		token_hash TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		last_seen INTEGER NOT NULL DEFAULT 0,
		hostname TEXT NOT NULL DEFAULT '',
		version TEXT NOT NULL DEFAULT '',
		jobs_json TEXT NOT NULL DEFAULT '[]',
		checks_json TEXT NOT NULL DEFAULT '[]',
		running_config INTEGER NOT NULL DEFAULT 0,
		config_status_version INTEGER NOT NULL DEFAULT 0,
		config_status_error TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE backup_runs (
		agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		remote_id INTEGER NOT NULL,
		job TEXT NOT NULL,
		repository TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		finished_at INTEGER NOT NULL,
		success INTEGER NOT NULL,
		snapshot_id TEXT NOT NULL,
		bytes_added INTEGER NOT NULL,
		total_bytes INTEGER NOT NULL,
		error TEXT NOT NULL,
		PRIMARY KEY (agent_id, remote_id)
	);
	CREATE INDEX backup_runs_job ON backup_runs(agent_id, job, repository, finished_at);
	CREATE TABLE check_results (
		agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		remote_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		at INTEGER NOT NULL,
		ok INTEGER NOT NULL,
		latency_ms INTEGER NOT NULL,
		error TEXT NOT NULL,
		PRIMARY KEY (agent_id, remote_id)
	);
	CREATE INDEX check_results_name ON check_results(agent_id, name, at);
	CREATE TABLE metrics (
		agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		remote_id INTEGER NOT NULL,
		at INTEGER NOT NULL,
		cpu REAL NOT NULL,
		memory REAL NOT NULL,
		swap REAL NOT NULL,
		load1 REAL NOT NULL,
		disk_max REAL NOT NULL,
		PRIMARY KEY (agent_id, remote_id)
	);
	CREATE INDEX metrics_at ON metrics(agent_id, at);
	CREATE TABLE agent_alerts (
		agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		key TEXT NOT NULL,
		since INTEGER NOT NULL,
		message TEXT NOT NULL,
		PRIMARY KEY (agent_id, key)
	);
	CREATE TABLE commands (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		type TEXT NOT NULL,
		args TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		started_at INTEGER NOT NULL DEFAULT 0,
		finished_at INTEGER NOT NULL DEFAULT 0,
		output TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX commands_agent ON commands(agent_id, status, id);
	CREATE TABLE configs (
		agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
		version INTEGER NOT NULL,
		yaml TEXT NOT NULL,
		comment TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (agent_id, version)
	);
	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		csrf TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	);`,
}

func openDB(path string) (*sql.DB, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
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
