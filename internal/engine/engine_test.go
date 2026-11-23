package engine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"sentinel/internal/config"
)

func postgresJob() *config.Backup {
	return &config.Backup{
		Name: "db", Type: config.BackupPostgres,
		Postgres: &config.Postgres{Host: "db.local", Port: 5433, User: "app", Password: "pg-secret", Database: "shop"},
	}
}

func TestForKnowsDatabaseTypesOnly(t *testing.T) {
	if _, ok := For(config.BackupPostgres); !ok {
		t.Fatal("postgres engine not registered")
	}
	if IsDatabase(config.BackupFiles) || IsDatabase("") || IsDatabase("oracle") {
		t.Fatal("non-database types must not resolve to an engine")
	}
}

func TestPostgresDump(t *testing.T) {
	pgEngine, _ := For(config.BackupPostgres)
	dump, err := pgEngine.Dump(postgresJob())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pg_dump", "--format=custom", "--no-password", "--host", "db.local", "--port", "5433", "--username", "app", "--dbname", "shop"}
	if !slices.Equal(dump.Command, want) {
		t.Errorf("command = %v, want %v", dump.Command, want)
	}
	if dump.Filename != "shop.dump" {
		t.Errorf("filename = %q", dump.Filename)
	}
	if !slices.Equal(dump.Env, []string{"PGPASSWORD=pg-secret"}) {
		t.Errorf("env = %v", dump.Env)
	}
}

func TestPostgresDumpWithoutPasswordSetsNoEnv(t *testing.T) {
	job := postgresJob()
	job.Postgres.Password = ""
	pgEngine, _ := For(config.BackupPostgres)
	dump, err := pgEngine.Dump(job)
	if err != nil || len(dump.Env) != 0 {
		t.Fatalf("env = %v, err = %v", dump.Env, err)
	}
}

func TestPostgresDumpRequiresSettings(t *testing.T) {
	pgEngine, _ := For(config.BackupPostgres)
	if _, err := pgEngine.Dump(&config.Backup{Type: config.BackupPostgres}); err == nil {
		t.Fatal("want error when postgres settings are missing")
	}
	if got := pgEngine.Database(&config.Backup{Type: config.BackupPostgres}); got != "" {
		t.Errorf("database = %q", got)
	}
}

func TestPostgresRestoreHint(t *testing.T) {
	pgEngine, _ := For(config.BackupPostgres)
	got := pgEngine.RestoreHint(postgresJob(), "/tmp/r")
	if want := "pg_restore --clean --if-exists -d shop /tmp/r/shop.dump"; got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
}

func TestToolsForDeduplicates(t *testing.T) {
	backups := []config.Backup{
		{Type: config.BackupFiles},
		*postgresJob(),
		*postgresJob(),
	}
	if got := ToolsFor(backups); !slices.Equal(got, []string{"pg_dump"}) {
		t.Errorf("tools = %v", got)
	}
	if got := ToolsFor([]config.Backup{{Type: config.BackupFiles}}); len(got) != 0 {
		t.Errorf("files-only config needs no tools, got %v", got)
	}
}

func TestCapabilitiesListEngines(t *testing.T) {
	if got := Capabilities(); !slices.Equal(got, []string{"engine:mongodb", "engine:mysql", "engine:postgres"}) {
		t.Errorf("capabilities = %v", got)
	}
}

func TestShellQuote(t *testing.T) {
	for value, want := range map[string]string{
		"/tmp/r/shop.dump": "/tmp/r/shop.dump",
		"/tmp/my restore":  "'/tmp/my restore'",
		"it's":             `'it'\''s'`,
		"crm.*":            "'crm.*'",
		"$(rm -rf /)":      "'$(rm -rf /)'",
	} {
		if got := shellQuote(value); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", value, got, want)
		}
	}
	pgEngine, _ := For(config.BackupPostgres)
	if got := pgEngine.RestoreHint(postgresJob(), "/tmp/my restore"); got != "pg_restore --clean --if-exists -d shop '/tmp/my restore/shop.dump'" {
		t.Errorf("hint = %q", got)
	}
}

func TestSweepStaleSecretFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	old := filepath.Join(tmp, "sentinel-mysql-111.cnf")
	fresh := filepath.Join(tmp, "sentinel-mongo-222.yaml")
	other := filepath.Join(tmp, "unrelated.cnf")
	for _, path := range []string{old, fresh, other} {
		if err := os.WriteFile(path, []byte("password=x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(old, past, past)
	os.Chtimes(other, past, past)

	if removed := SweepStaleSecretFiles(24 * time.Hour); removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("stale credential file kept")
	}
	for _, kept := range []string{fresh, other} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s must be kept: %v", kept, err)
		}
	}
}
