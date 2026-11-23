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
	`ALTER TABLE agents ADD COLUMN beszel_system TEXT NOT NULL DEFAULT '';`,
	`DROP TABLE metrics;`,
	`ALTER TABLE agents ADD COLUMN beszel_owned INTEGER NOT NULL DEFAULT 0;`,
	// Users, roles and the audit trail. user_id 0 is the bootstrap admin from server.yaml.
	`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		username TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);
	ALTER TABLE sessions ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE sessions ADD COLUMN username TEXT NOT NULL DEFAULT '';
	ALTER TABLE commands ADD COLUMN requested_by TEXT NOT NULL DEFAULT '';
	ALTER TABLE configs ADD COLUMN author TEXT NOT NULL DEFAULT '';
	CREATE TABLE audit_log (
		id INTEGER PRIMARY KEY,
		at INTEGER NOT NULL,
		user_id INTEGER NOT NULL DEFAULT 0,
		username TEXT NOT NULL,
		ip TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL,
		agent_id INTEGER NOT NULL DEFAULT 0,
		agent_name TEXT NOT NULL DEFAULT '',
		job TEXT NOT NULL DEFAULT '',
		details TEXT NOT NULL DEFAULT '{}',
		outcome TEXT NOT NULL
	);
	CREATE INDEX audit_log_at ON audit_log (at);
	CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
		BEGIN SELECT RAISE(ABORT, 'audit log is append-only'); END;
	CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
		BEGIN SELECT RAISE(ABORT, 'audit log is append-only'); END;`,
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
