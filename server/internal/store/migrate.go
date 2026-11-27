package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// The migrations are SQL files named NNNN_name.sql, applied in order of NNNN. They are only ever
// added: a file that was applied is never edited, renamed or removed, because a database that ran
// the old text and one that runs the new would differ. The checksum of every applied file is kept in
// the database and checked at each start.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

// loadMigrations reads the embedded files, checking that they number 1, 2, 3 … without a gap.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var migrations []migration
	for _, entry := range entries {
		match := migrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("migration %q is not named NNNN_name.sql", entry.Name())
		}
		body, err := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		version, _ := strconv.Atoi(match[1])
		sum := sha256.Sum256(body)
		migrations = append(migrations, migration{version: version, name: entry.Name(), sql: string(body), checksum: hex.EncodeToString(sum[:])})
	}
	slices.SortFunc(migrations, func(a, b migration) int { return a.version - b.version })
	for i, m := range migrations {
		if m.version != i+1 {
			return nil, fmt.Errorf("migrations are not numbered 1, 2, 3 …: %s is number %d, want %d", m.name, m.version, i+1)
		}
	}
	return migrations, nil
}

// migrate applies the migrations the database has not seen.
func migrate(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL) STRICT`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	checksumByVersion, err := appliedChecksums(ctx, db)
	if err != nil {
		return err
	}
	for version := range checksumByVersion {
		if version > len(migrations) {
			return fmt.Errorf("the database has migration %d, but this sentinel-server knows only %d: it was made by a newer version", version, len(migrations))
		}
	}
	for _, m := range migrations {
		applied, ok := checksumByVersion[m.version]
		if ok && applied != m.checksum {
			return fmt.Errorf("migration %s was changed after it was applied; add a new migration instead", m.name)
		}
		if ok {
			continue
		}
		if err := apply(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func appliedChecksums(ctx context.Context, db *sql.DB) (map[int]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	checksumByVersion := map[int]string{}
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		checksumByVersion[version] = checksum
	}
	return checksumByVersion, rows.Err()
}

func apply(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.version, m.name, m.checksum, time.Now().Unix()); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	return tx.Commit()
}
