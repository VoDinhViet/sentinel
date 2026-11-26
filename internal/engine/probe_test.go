package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/internal/config"
)

// fakeTool puts an executable shell script named name first on PATH.
func fakeTool(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestProbeSucceedsOnTheFirstByteAndStopsTheTool(t *testing.T) {
	// The tool writes a byte, then would dump for a minute: the probe must not wait for it.
	fakeTool(t, "pg_dump", `printf 'P'; sleep 60`)
	start := time.Now()
	if err := Probe(t.Context(), postgresJob(), nil); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the probe waited %s for a dump it does not need", took)
	}
}

func TestProbeReportsWhyTheToolFailed(t *testing.T) {
	fakeTool(t, "pg_dump", `echo 'pg_dump: error: connection to server at "db" failed: FATAL:  password authentication failed for user "app"' >&2; exit 1`)
	err := Probe(t.Context(), postgresJob(), nil)
	if err == nil || !strings.Contains(err.Error(), "password authentication failed") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "pg-secret") || strings.HasPrefix(err.Error(), "pg_dump: pg_dump") || strings.HasPrefix(err.Error(), "error:") {
		t.Errorf("the message leaks or repeats itself: %v", err)
	}
}

// The dump tool of a probe gets the same environment as a backup's: not the variables the config refers to.
func TestProbeDoesNotHandOverVariablesTheConfigRefersTo(t *testing.T) {
	t.Setenv("NOTIFIER_TOKEN", "tg-secret")
	fakeTool(t, "pg_dump", `[ -z "$NOTIFIER_TOKEN" ] || { echo "saw the notifier token" >&2; exit 1; }; printf 'P'; sleep 60`)

	if err := Probe(t.Context(), postgresJob(), []string{"NOTIFIER_TOKEN"}); err != nil {
		t.Errorf("the tool saw a variable the config refers to: %v", err)
	}
	// Without the filter the same tool sees the token, so the fake tool does check what it claims to.
	if err := Probe(t.Context(), postgresJob(), nil); err == nil || !strings.Contains(err.Error(), "saw the notifier token") {
		t.Errorf("control run: err = %v, want the tool to see the token", err)
	}
}

func TestProbeSaysWhenTheToolIsNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := Probe(t.Context(), postgresJob(), nil); err == nil || !strings.Contains(err.Error(), "pg_dump is not installed") {
		t.Errorf("err = %v", err)
	}
}

func TestProbeGivesUpWhenNothingAnswers(t *testing.T) {
	fakeTool(t, "pg_dump", `sleep 60`)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := Probe(ctx, postgresJob(), nil); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Errorf("err = %v", err)
	}
}

func TestProbeIsRefusedForFiles(t *testing.T) {
	if err := Probe(t.Context(), &config.Job{Type: config.JobFiles}, nil); err == nil {
		t.Error("a files job has no database to probe")
	}
}

func TestMySQLProbeRunsALightDumpToTheEndAndChecksTheExitStatus(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	// mysqldump writes its header before it selects the database, so a byte proves nothing:
	// the tool prints a header, then fails.
	fakeTool(t, "mariadb-dump", `printf '%s\n' "$@" > `+argsFile+`; echo '-- header'; echo 'Warning: noise' >&2; echo "mariadb-dump: Got error: 1049: Unknown database 'shop' when selecting the database" >&2; exit 2`)
	err := Probe(t.Context(), mysqlJob(), nil)
	if err == nil || !strings.Contains(err.Error(), "1049") || strings.Contains(err.Error(), "noise") {
		t.Fatalf("a header followed by an error is a failure without the warnings: %v", err)
	}
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--no-data", "--no-create-info", "--skip-routines", "--skip-triggers", "--skip-events"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("the probe dump lacks %s:\n%s", want, args)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(string(args)), "--\nshop") {
		t.Errorf("the database stays after --:\n%s", args)
	}
	if leftover, _ := filepath.Glob(filepath.Join(os.TempDir(), "sentinel-mysql-*.cnf")); len(leftover) != 0 {
		t.Errorf("credentials files left behind: %v", leftover)
	}
	fakeTool(t, "mariadb-dump", `echo '-- header'; echo '-- Dump completed'`)
	if err := Probe(t.Context(), mysqlJob(), nil); err != nil {
		t.Errorf("a clean run is a success: %v", err)
	}
}
