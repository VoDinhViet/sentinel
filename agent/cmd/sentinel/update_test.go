package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"sentinel/agent/internal/selfupdate"
	"sentinel/agent/internal/store"
)

func TestArmGuardStartsATimerThatRunsThePreviousBinaryWithFixedArguments(t *testing.T) {
	var name string
	var args []string
	old := runSystemTool
	t.Cleanup(func() { runSystemTool = old })
	runSystemTool = func(_ context.Context, program string, arguments ...string) (string, error) {
		name, args = program, arguments
		return "", nil
	}
	if err := armGuard(context.Background(), "/usr/local/bin/sentinel", "/etc/sentinel/config.yaml"); err != nil {
		t.Fatal(err)
	}
	got := name + " " + strings.Join(args, " ")
	want := "systemd-run --on-active=420 --unit=sentinel-self-update-guard --collect --quiet -- /usr/local/bin/sentinel.prev --config /etc/sentinel/config.yaml update-guard"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestTheHelperCommandsAreHiddenAndTakeNoArgument(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"update-install", "update-rollback", "update-guard"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd.Name() != name || !cmd.Hidden {
			t.Errorf("%s: %v hidden=%v", name, err, cmd != nil && cmd.Hidden)
			continue
		}
		if err := cmd.Args(cmd, []string{"/etc/passwd"}); err == nil {
			t.Errorf("%s took an argument", name)
		}
	}
}

func TestARollbackPutsTheCopyOfTheDatabaseBackOnlyWhenTheSchemaMovedOn(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state.db")
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("store: "+storePath+"\nrepositories: {}\njobs: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	updateDir := filepath.Join(dir, "update")
	if err := os.MkdirAll(updateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := snapshotState(context.Background(), storePath, updateDir); err != nil {
		t.Fatal(err)
	}
	app := &cliApp{configPath: configPath}
	// The schema did not move: the database is left alone.
	before, _ := os.ReadFile(storePath)
	if err := app.restoreState(updateDir); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(storePath); string(before) != string(after) {
		t.Error("a database of the same schema was replaced")
	}
	// The new version has moved it on: the copy is put back.
	raw, err := sql.Open("sqlite", storePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if err := app.restoreState(updateDir); err != nil {
		t.Fatal(err)
	}
	if version, err := store.SchemaVersion(storePath); err != nil || version != store.KnownSchema() {
		t.Errorf("schema after the restore = %d, %v", version, err)
	}
	// No copy, nothing to do.
	if err := os.Remove(filepath.Join(updateDir, selfupdate.SnapshotName)); err != nil {
		t.Fatal(err)
	}
	if err := app.restoreState(updateDir); err != nil {
		t.Errorf("without a copy: %v", err)
	}
}

// guardPlant is an installed update on trial: the new binary in place, the previous one kept, the note written.
func guardPlant(t *testing.T) (app *cliApp, exe, updateDir string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("store: "+filepath.Join(dir, "state.db")+"\nrepositories: {}\njobs: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe = filepath.Join(dir, "sentinel")
	for path, content := range map[string]string{exe: "new binary", exe + ".prev": "old binary"} {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil { //nolint:gosec // a test file
			t.Fatal(err)
		}
	}
	updateDir = filepath.Join(dir, "update")
	if err := os.MkdirAll(updateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := selfupdate.WriteMarker(updateDir, selfupdate.Marker{State: selfupdate.Installed, From: "1.1.0", To: "1.2.0", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	old := executablePath
	t.Cleanup(func() { executablePath = old })
	executablePath = func() (string, error) { return exe + ".prev", nil } // the guard runs the previous binary
	return &cliApp{configPath: configPath}, exe, updateDir
}

func TestTheGuardPutsThePreviousBinaryBackAndRestartsTheDaemonWhenTheNewVersionNeverReported(t *testing.T) {
	app, exe, _ := guardPlant(t)
	var calls []string
	old := runSystemTool
	t.Cleanup(func() { runSystemTool = old })
	runSystemTool = func(_ context.Context, program string, arguments ...string) (string, error) {
		calls = append(calls, program+" "+strings.Join(arguments, " "))
		return "", nil
	}
	cmd := app.updateGuardCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "old binary" {
		t.Errorf("the binary is %q", data)
	}
	if len(calls) != 1 || calls[0] != "systemctl restart -- sentinel.service" {
		t.Errorf("calls = %v", calls)
	}
	// Run again (the timer fires once, but a second run must do no harm): the note is a rollback now.
	calls = nil
	if err := cmd.RunE(cmd, nil); err != nil || len(calls) != 0 {
		t.Errorf("a second run: %v %v", err, calls)
	}
}

func TestTheGuardDoesNothingWhenTheNewVersionReported(t *testing.T) {
	app, exe, updateDir := guardPlant(t)
	selfupdate.Confirm(updateDir) // the new version reached the control plane
	old := runSystemTool
	t.Cleanup(func() { runSystemTool = old })
	runSystemTool = func(context.Context, string, ...string) (string, error) {
		t.Error("the daemon was restarted although the new version reported")
		return "", nil
	}
	cmd := app.updateGuardCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "new binary" {
		t.Errorf("the binary is %q", data)
	}
}
