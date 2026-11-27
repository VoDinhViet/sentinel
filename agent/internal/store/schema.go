package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// KnownSchema is the schema version this binary writes: the number of its migrations.
func KnownSchema() int { return len(migrations) }

// SchemaVersion reads the schema version of the state database at path without migrating it.
func SchemaVersion(path string) (int, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
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
// process may be using the database. Only its owner can read the copy.
func Snapshot(ctx context.Context, path, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s exists", dest)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dest); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("copy the state database: %w", err)
	}
	return os.Chmod(dest, 0o600)
}

// Restore puts the copy made by Snapshot in the place of the state database at path. The daemon is not expected
// to be using it for long: it is about to be restarted. What the old file and its WAL held is dropped.
func Restore(snapshot, path string) error {
	in, err := os.Open(snapshot)
	if err != nil {
		return err
	}
	defer in.Close()
	temp := path + ".restore"
	out, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(temp)
		return err
	}
	if err := errors.Join(out.Sync(), out.Close()); err != nil {
		_ = os.Remove(temp)
		return err
	}
	for _, suffix := range walSuffixes {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = os.Remove(temp)
			return err
		}
	}
	return os.Rename(temp, path)
}
