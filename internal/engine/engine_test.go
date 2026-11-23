package engine

import (
	"slices"
	"testing"

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
