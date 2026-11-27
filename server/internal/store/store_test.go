package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.db")
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

// knownChecksums are the SHA-256 of every migration that has been released. The migrations are
// append-only: when you add one, add its line here, and never change a line that is already here.
var knownChecksums = map[string]string{
	"0001_init.sql":        "b57dd67ded93ba14e84f938e378e4695097e20377bc1434d3bec10064b0c28a5",
	"0002_login_table.sql": "54333b73932c2e0816e3ac8d8f6f8e4ccb4f845bb1d8921e3a34a01d2e866d99",
}

func TestMigrationsAreAppendOnly(t *testing.T) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry.Name()] = true
		body, _ := fs.ReadFile(migrationFiles, "migrations/"+entry.Name())
		sum := sha256.Sum256(body)
		got := hex.EncodeToString(sum[:])
		want, known := knownChecksums[entry.Name()]
		switch {
		case !known:
			t.Errorf("migration %s is not in knownChecksums: add %q", entry.Name(), got)
		case want != got:
			t.Errorf("migration %s was edited: its checksum is %s, it was released as %s", entry.Name(), got, want)
		}
	}
	for name := range knownChecksums {
		if !seen[name] {
			t.Errorf("migration %s was released and is gone", name)
		}
	}
}

func TestOpenCreatesAPrivateDatabaseAndIsIdempotent(t *testing.T) {
	st, path := openTemp(t)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("database mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	for _, table := range []string{"agents", "join_tokens", "certs", "login", "sessions", "login_failures", "audit_log", "alerts"} {
		var name string
		if err := st.DB.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Errorf("no table %s: %v", table, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("opening again: %v", err)
	}
	defer again.Close()
	var applied int
	if err := again.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil || applied != len(knownChecksums) {
		t.Errorf("migrations applied = %d, %v; want %d, each once", applied, err, len(knownChecksums))
	}
}

func TestOpenRefusesAnEditedMigrationAndANewerDatabase(t *testing.T) {
	st, path := openTemp(t)
	if _, err := st.DB.Exec(`UPDATE schema_migrations SET checksum = 'edited' WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := Open(context.Background(), path); err == nil || !strings.Contains(err.Error(), "was changed after it was applied") {
		t.Errorf("edited migration: err = %v", err)
	}

	st, path = openTemp(t)
	if _, err := st.DB.Exec(`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (99, '0099_future.sql', 'x', 0)`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := Open(context.Background(), path); err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Errorf("newer database: err = %v", err)
	}
}

func TestInTxRollsBackOnError(t *testing.T) {
	st, _ := openTemp(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := st.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agents (name, state, created_at) VALUES ('web-01', 'pending', 1)`); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var count int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&count); err != nil || count != 0 {
		t.Errorf("agents = %d, %v; the failed transaction left a row", count, err)
	}
}

func TestAuditLogCannotBeChangedOrDeleted(t *testing.T) {
	st, _ := openTemp(t)
	if _, err := st.DB.Exec(`INSERT INTO audit_log (at, actor, session, ip, action, target, detail, prev_hash, hash) VALUES (1, 'ctl', '', '', 'x', '', '', '', 'h')`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`UPDATE audit_log SET actor = 'someone'`, `DELETE FROM audit_log`} {
		if _, err := st.DB.Exec(statement); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: err = %v, want the append-only refusal", statement, err)
		}
	}
}

func TestOnlyOneLoginRow(t *testing.T) {
	st, _ := openTemp(t)
	if _, err := st.DB.Exec(`INSERT INTO login (id, password_hash, changed_at) VALUES (1, 'h', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO login (id, password_hash, changed_at) VALUES (2, 'h', 1)`); err == nil {
		t.Error("a second login row was stored")
	}
}
