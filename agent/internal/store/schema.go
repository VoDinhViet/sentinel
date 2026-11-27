package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"syscall"
)

// KnownSchema is the schema version this binary writes: the number of its migrations.
func KnownSchema() int { return len(migrations) }

// readOnlyDSN opens the file at path read-only, whatever characters its name has.
func readOnlyDSN(path string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(5000)"
}

// SchemaVersion reads the schema version of the state database at path without migrating it, and without creating
// it when it is not there.
func SchemaVersion(path string) (int, error) {
	if _, err := os.Stat(path); err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read the schema version of %s: %w", path, err)
	}
	return version, nil
}

// Snapshot writes a consistent copy of the state database at path to dest, which must not exist, while another
// process may be using the database. Only its owner can read the copy, from the moment it exists.
func Snapshot(ctx context.Context, path, dest string) error {
	// O_EXCL also refuses a link left at dest; SQLite accepts an output file that exists and is empty.
	empty, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := empty.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		_ = os.Remove(dest)
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("copy the state database: %w", err)
	}
	return nil
}

// RolledBackSuffix is added to the name of a database that a restore put aside.
const RolledBackSuffix = ".rolled-back"

// Restore puts the copy made by Snapshot in the place of the state database at path, and keeps what was there as
// path+RolledBackSuffix (a daemon that still has it open goes on writing to that file, which is then not lost). The
// copy is moved, not copied: no space is needed. The write-ahead files of the old database are removed so that they
// are not replayed on the copy. It is for a daemon that is about to be restarted.
func Restore(snapshot, path string) error {
	info, err := os.Lstat(snapshot)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", snapshot)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the user that restores it", snapshot)
	}
	aside := path + RolledBackSuffix
	_ = os.Remove(aside)
	if err := os.Rename(path, aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("put the database aside: %w", err)
	}
	for _, suffix := range walSuffixes {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(snapshot, path); err != nil {
		_ = os.Rename(aside, path) // not moved: the database stays where it was
		return fmt.Errorf("put the copy in place: %w", err)
	}
	return nil
}
