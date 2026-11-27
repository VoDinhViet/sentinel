// Package store is the SQLite database of the control plane: opening it, the migrations that build it,
// and a transaction helper. The queries live with the packages that own the tables.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "modernc.org/sqlite" // the driver, registered as "sqlite"
)

// Store is the open database.
type Store struct {
	DB *sql.DB
}

// Querier is what *sql.DB and *sql.Tx both are: code that must work inside a transaction or outside
// one takes this.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Open opens the database at path, creating it (mode 0600) if it is not there, and brings it up to the
// newest schema. It fails if the database was made by a newer sentinel-server, or if a migration it
// already applied has been edited since.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := createPrivate(path); err != nil {
		return nil, err
	}
	// Writers take the lock when the transaction begins, so two of them wait for each other instead
	// of failing half way; a reader never waits for a writer (WAL).
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	store := &Store{DB: db}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// createPrivate makes an empty database file that only its owner can read, so the WAL files SQLite
// adds beside it get the same mode.
func createPrivate(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open database %s: %w", path, err)
	}
	return file.Close()
}

func (s *Store) Close() error { return s.DB.Close() }

// InTx runs fn in one transaction: committed when fn returns nil, rolled back otherwise.
func (s *Store) InTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, rollbackErr)
			}
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
