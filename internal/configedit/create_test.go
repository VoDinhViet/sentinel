package configedit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/config"
)

func initialFor() Initial {
	return Initial{
		ServerName: "web-01",
		Store:      "${HOME}/.local/state/sentinel/state.db",
		Repository: Repository{Name: "local", URL: "${HOME}/.local/share/sentinel/repo", PasswordEnv: "RESTIC_PASSWORD", Env: map[string]string{}},
		Job:        Job{Name: "documents", Type: config.JobFiles, Paths: []string{"${HOME}/Documents"}},
		Schedule:   "daily 02:00",
		Retention:  config.Retention{Daily: 7, Weekly: 4, Monthly: 6},
	}
}

func TestCreateWritesACompactConfigThatParses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentinel", "config.yaml")
	if err := Create(path, initialFor(), false); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, path)
	for _, want := range []string{"written by `sentinel init`", "server_name: web-01", "store: ${HOME}/.local/state/sentinel/state.db", "local:", `"${RESTIC_PASSWORD}"`, "schedule: daily 02:00", "daily: 7", "name: documents", `paths: ['${HOME}/Documents']`} {
		if !strings.Contains(written, want) {
			t.Errorf("config lacks %q:\n%s", want, written)
		}
	}
	jobPart := written[strings.Index(written, "backups:"):]
	for _, repeated := range []string{"repositories", "retention", "schedule"} {
		if strings.Contains(jobPart, repeated) {
			t.Errorf("the job must inherit, not repeat %q:\n%s", repeated, jobPart)
		}
	}
	t.Setenv("HOME", "/home/someone")
	t.Setenv("RESTIC_PASSWORD", "x")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the written config does not load: %v\n%s", err, written)
	}
	job := cfg.Jobs[0]
	if job.Schedule != "daily 02:00" || len(job.Repositories) != 1 || job.Repositories[0] != "local" || job.Retention.Monthly != 6 || !job.Inherited.Repositories {
		t.Errorf("job = %+v", job)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a fresh config leaves no .bak behind")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}

func TestCreateRefusesToOverwriteUnlessForced(t *testing.T) {
	path := writeConfig(t, baseConfig)
	if err := Create(path, initialFor(), false); !errors.Is(err, ErrConfigExists) {
		t.Fatalf("overwriting = %v, want ErrConfigExists", err)
	}
	if readFile(t, path) != baseConfig {
		t.Error("a refused create must not touch the file")
	}
	if err := Create(path, initialFor(), true); err != nil {
		t.Fatal(err)
	}
	if readFile(t, path+".old") != baseConfig || !strings.Contains(readFile(t, path), "documents") {
		t.Error("--force keeps the old file as .old and writes the new one")
	}
}

func TestCreateLeavesNothingWhenTheInputIsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	bad := initialFor()
	bad.Job.Paths = nil
	var invalid *InvalidError
	if err := Create(path, bad, false); !errors.As(err, &invalid) || invalid.Field != "paths" {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a half-written config must be removed")
	}
}

func TestEnsureSecretAddsOnlyWhatIsMissing(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "etc", "sentinel.env")
	created, err := EnsureSecret(envFile, "RESTIC_PASSWORD")
	if err != nil || !created {
		t.Fatalf("first call: %v %v", created, err)
	}
	if info, _ := os.Stat(envFile); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, the env file holds secrets", info.Mode().Perm())
	}
	first := readFile(t, envFile)
	if !strings.HasPrefix(first, "RESTIC_PASSWORD=") || len(strings.TrimSpace(first)) < 40 {
		t.Errorf("env file = %q", first)
	}
	if created, err = EnsureSecret(envFile, "RESTIC_PASSWORD"); err != nil || created || readFile(t, envFile) != first {
		t.Errorf("an existing value must stay: %v %v", created, err)
	}
	if err := os.WriteFile(envFile, []byte("OTHER=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if created, err = EnsureSecret(envFile, "PG_PASSWORD"); err != nil || !created || !strings.HasPrefix(readFile(t, envFile), "OTHER=1\nPG_PASSWORD=") {
		t.Errorf("appending to a file without a final newline: %q %v", readFile(t, envFile), err)
	}
	if _, err := EnsureSecret(envFile, "bad name"); err == nil {
		t.Error("a bad variable name must be refused")
	}
}
