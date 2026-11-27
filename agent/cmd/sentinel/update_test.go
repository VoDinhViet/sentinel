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

func TestArmGuardStartsAServiceThatRunsThePreviousBinaryWithFixedArguments(t *testing.T) {
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
	want := "systemd-run --unit=sentinel-self-update-guard --collect --quiet -- /usr/local/bin/sentinel.prev --config /etc/sentinel/config.yaml update-guard"
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
	if _, err := os.Stat(storePath + store.RolledBackSuffix); err != nil {
		t.Errorf("the database that was replaced was not kept: %v", err)
	}
	// The copy was used: nothing to do.
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

// quickGuard makes the guard poll and give up waiting in milliseconds, with a daemon that is gone.
func quickGuard(t *testing.T, oldRuns func() bool) {
	t.Helper()
	oldEvery, oldPatience, oldRuns0 := guardEvery, guardPatience, oldDaemonRuns
	t.Cleanup(func() { guardEvery, guardPatience, oldDaemonRuns = oldEvery, oldPatience, oldRuns0 })
	guardEvery, guardPatience = time.Millisecond, 5*time.Millisecond
	oldDaemonRuns = func(context.Context, time.Time) bool { return oldRuns() }
}

func recordSystemCalls(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	old := runSystemTool
	t.Cleanup(func() { runSystemTool = old })
	runSystemTool = func(_ context.Context, program string, arguments ...string) (string, error) {
		calls = append(calls, program+" "+strings.Join(arguments, " "))
		return "", nil
	}
	return &calls
}

func runGuard(t *testing.T, app *cliApp) error {
	t.Helper()
	cmd := app.updateGuardCmd()
	cmd.SetContext(context.Background())
	return cmd.RunE(cmd, nil)
}

func TestTheGuardPutsThePreviousBinaryBackAndRestartsTheDaemonWhenTheNewVersionNeverReported(t *testing.T) {
	app, exe, _ := guardPlant(t)
	quickGuard(t, func() bool { return false })
	calls := recordSystemCalls(t)
	if err := runGuard(t, app); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "old binary" {
		t.Errorf("the binary is %q", data)
	}
	if len(*calls) != 1 || (*calls)[0] != "systemctl restart -- sentinel.service" {
		t.Errorf("calls = %v", *calls)
	}
	*calls = nil // a second run does no harm: the note is a rollback now
	if err := runGuard(t, app); err != nil || len(*calls) != 0 {
		t.Errorf("a second run: %v %v", err, *calls)
	}
}

func TestTheGuardWaitsWhileTheOldDaemonIsStillFinishingItsBackups(t *testing.T) {
	app, exe, _ := guardPlant(t)
	polls := 0
	quickGuard(t, func() bool { polls++; return polls <= 20 }) // the old daemon exits after 20 polls
	calls := recordSystemCalls(t)
	start := time.Now()
	if err := runGuard(t, app); err != nil {
		t.Fatal(err)
	}
	if polls <= 20 || time.Since(start) < 20*time.Millisecond {
		t.Errorf("the guard did not wait for the old daemon: %d polls", polls)
	}
	if data, _ := os.ReadFile(exe); string(data) != "old binary" || len(*calls) != 1 {
		t.Errorf("binary %q, calls %v", data, *calls)
	}
}

func TestTheGuardDoesNothingWhenTheNewVersionReported(t *testing.T) {
	app, exe, updateDir := guardPlant(t)
	selfupdate.Confirm(updateDir) // the new version reached the control plane
	quickGuard(t, func() bool { return false })
	calls := recordSystemCalls(t)
	if err := runGuard(t, app); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(exe); string(data) != "new binary" || len(*calls) != 0 {
		t.Errorf("binary %q, calls %v", data, *calls)
	}
}

func TestARollbackThatCannotHappenLeavesTheStateDatabaseAlone(t *testing.T) {
	app, exe, updateDir := guardPlant(t)
	if err := os.Remove(exe + ".prev"); err != nil { // no previous binary
		t.Fatal(err)
	}
	if err := app.goBack(updateDir, exe, "x"); err == nil {
		t.Error("a rollback without a previous binary succeeded")
	}
	if _, err := os.Stat(filepath.Join(updateDir, "..", "state.db"+store.RolledBackSuffix)); err == nil {
		t.Error("the database was touched")
	}
}

func TestStartTicksReadsTheStartOfAProcessWhoseNameHasBracketsAndSpaces(t *testing.T) {
	stat := "1234 (a b) c) S 1 1234 1234 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 98765 1000 100 18446744073709551615"
	if ticks, err := startTicks(stat); err != nil || ticks != 98765 {
		t.Errorf("ticks = %d, %v", ticks, err)
	}
	if _, err := startTicks("garbage"); err == nil {
		t.Error("garbage was read")
	}
	if started, err := processStart(os.Getpid()); err != nil || started.After(time.Now()) || time.Since(started) > 24*time.Hour*365 {
		t.Errorf("this process started %v, %v", started, err)
	}
}
