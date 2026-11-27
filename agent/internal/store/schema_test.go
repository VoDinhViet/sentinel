package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newerSchema makes the database at path look as a newer sentinel left it.
func newerSchema(t *testing.T, path string) {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`PRAGMA user_version = ` + "99"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestARestoredCopyLetsTheOlderBinaryOpenTheDatabaseAgain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordBackup(context.Background(), BackupRun{Job: "db", Repository: "s3", StartedAt: time.Now(), FinishedAt: time.Now(), Success: true}); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(dir, "state.prev.db")
	if err := Snapshot(context.Background(), path, snapshot); err != nil { // the daemon still has the database open
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(snapshot); info.Mode().Perm() != 0o600 {
		t.Errorf("the copy has mode %v", info.Mode().Perm())
	}
	if version, err := SchemaVersion(snapshot); err != nil || version != KnownSchema() {
		t.Errorf("copy schema = %d, %v", version, err)
	}
	newerSchema(t, path)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer sentinel") {
		t.Fatalf("the older binary opened a database of a newer schema: %v", err)
	}
	if err := Restore(snapshot, path); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatalf("still cannot open it: %v", err)
	}
	defer again.Close()
	if runs, err := again.LatestBackups(context.Background()); err != nil || len(runs) != 1 {
		t.Errorf("the data of the copy is gone: %v %v", runs, err)
	}
	if _, err := os.Stat(snapshot); err == nil {
		t.Error("the copy was copied, not moved")
	}
	if version, err := SchemaVersion(path + RolledBackSuffix); err != nil || version != 99 {
		t.Errorf("the database that was put aside: %d, %v", version, err)
	}
}

func TestRestoreRefusesALinkAndLeavesTheDatabaseWhereItWas(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	link := filepath.Join(dir, "copy.db")
	if err := os.Symlink("/etc/passwd", link); err != nil {
		t.Fatal(err)
	}
	if err := Restore(link, path); err == nil {
		t.Error("a link was restored")
	}
	if _, err := SchemaVersion(path); err != nil {
		t.Errorf("the database was disturbed: %v", err)
	}
	if err := Restore(filepath.Join(dir, "missing.db"), path); err == nil {
		t.Error("a copy that is not there was restored")
	}
}

func TestSchemaVersionDoesNotCreateAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nothing.db")
	if _, err := SchemaVersion(path); err == nil {
		t.Error("a database that is not there has a version")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("reading a version made a file")
	}
}

func TestSnapshotRefusesToOverwriteAndQuotesItsPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	odd := filepath.Join(dir, "it's a copy.db")
	if err := Snapshot(context.Background(), path, odd); err != nil {
		t.Fatalf("a path with a quote: %v", err)
	}
	if err := Snapshot(context.Background(), path, odd); err == nil {
		t.Error("a copy was overwritten")
	}
}
