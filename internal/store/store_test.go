package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The store holds restic and dump error text, so no other local user may read it, whatever the umask
// of the process is and whether or not the file existed before.
func TestOpenKeepsFilesPrivate(t *testing.T) {
	oldUmask := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	tests := []struct {
		name    string
		prepare func(t *testing.T, path string)
	}{
		{"new database", func(*testing.T, string) {}},
		{"existing world-readable database", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			tt.prepare(t, path)
			st, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.RecordBackup(context.Background(), BackupRun{Job: "db", Repository: "local", Success: false, Error: "boom"}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{path, path + "-wal", path + "-shm"} {
				info, err := os.Stat(name)
				if err != nil {
					t.Fatalf("%s: %v", filepath.Base(name), err)
				}
				if mode := info.Mode().Perm(); mode != 0o600 {
					t.Errorf("%s has mode %o, want 600", filepath.Base(name), mode)
				}
			}
		})
	}
}

func TestMigrateTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		s.Close()
	}
}

func TestBackupRuns(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	if _, ok, err := s.LastSuccessfulBackup(ctx, "db"); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}

	t1 := time.Unix(1_700_000_000, 0)
	t2 := t1.Add(time.Hour)
	runs := []BackupRun{
		{Job: "db", Repository: "s3", StartedAt: t1, FinishedAt: t1, Success: true, SnapshotID: "aaa"},
		{Job: "db", Repository: "s3", StartedAt: t2, FinishedAt: t2, Success: false, Error: "boom"},
		{Job: "db", Repository: "gdrive", StartedAt: t1, FinishedAt: t1, Success: true, SnapshotID: "bbb"},
	}
	for _, run := range runs {
		if err := s.RecordBackup(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	last, ok, err := s.LastSuccessfulBackup(ctx, "db")
	if err != nil || !ok || !last.Equal(t1) {
		t.Fatalf("last success = %v, %v, %v; want %v", last, ok, err, t1)
	}

	latest, err := s.LatestBackups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("want 2 latest rows, got %+v", latest)
	}
	// Ordered by job, repository: gdrive before s3.
	if latest[0].Repository != "gdrive" || !latest[0].Success {
		t.Errorf("gdrive row = %+v", latest[0])
	}
	if latest[1].Repository != "s3" || latest[1].Success || latest[1].Error != "boom" {
		t.Errorf("s3 row should be the newer failed run, got %+v", latest[1])
	}
}

func TestAlerts(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Unix(1_700_000_000, 0)

	if err := s.SaveAlert(ctx, Alert{Key: "uptime:web", Firing: true, Since: now, LastNotified: now, Message: "down"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAlert(ctx, Alert{Key: "cpu", Firing: false, Since: now, LastNotified: now, Message: "ok"}); err != nil {
		t.Fatal(err)
	}
	// Upsert flips the first alert to resolved.
	if err := s.SaveAlert(ctx, Alert{Key: "uptime:web", Firing: false, Since: now.Add(time.Minute), LastNotified: now, Message: "up"}); err != nil {
		t.Fatal(err)
	}

	all, err := s.Alerts(ctx, false)
	if err != nil || len(all) != 2 {
		t.Fatalf("all alerts = %+v, %v", all, err)
	}
	firing, err := s.Alerts(ctx, true)
	if err != nil || len(firing) != 0 {
		t.Fatalf("firing alerts = %+v, %v", firing, err)
	}
	if all[1].Key != "uptime:web" || all[1].Message != "up" {
		t.Errorf("upsert not applied: %+v", all[1])
	}
}

func TestChecksPrune(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	old := time.Unix(1_600_000_000, 0)
	recent := time.Unix(1_700_000_000, 0)

	for _, result := range []CheckResult{
		{Name: "web", At: old, OK: false, Error: "timeout"},
		{Name: "web", At: recent, OK: true, Latency: 42 * time.Millisecond},
	} {
		if err := s.RecordCheck(ctx, result); err != nil {
			t.Fatal(err)
		}
	}

	checks, err := s.LatestChecks(ctx)
	if err != nil || len(checks) != 1 || !checks[0].OK || checks[0].Latency != 42*time.Millisecond {
		t.Fatalf("latest checks = %+v, %v", checks, err)
	}

	if err := s.Prune(ctx, recent.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM check_results`).Scan(&n)
	if n != 1 {
		t.Errorf("checks after prune = %d, want 1", n)
	}
}

func TestSinceCursors(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	at := time.Unix(1_700_000_000, 0)
	for i := 0; i < 5; i++ {
		s.RecordBackup(ctx, BackupRun{Job: "j", Repository: "r", StartedAt: at, FinishedAt: at, Success: true})
		s.RecordCheck(ctx, CheckResult{Name: "c", At: at, OK: true})
	}

	runs, err := s.BackupRunsSince(ctx, 0, 3)
	if err != nil || len(runs) != 3 || runs[0].ID != 1 || runs[2].ID != 3 {
		t.Fatalf("first page = %+v, %v", runs, err)
	}
	runs, _ = s.BackupRunsSince(ctx, runs[2].ID, 3)
	if len(runs) != 2 || runs[0].ID != 4 {
		t.Fatalf("second page = %+v", runs)
	}
	if runs, _ := s.BackupRunsSince(ctx, 5, 3); len(runs) != 0 {
		t.Fatalf("past the end = %+v", runs)
	}

	checks, err := s.ChecksSince(ctx, 2, 10)
	if err != nil || len(checks) != 3 || checks[0].ID != 3 {
		t.Fatalf("checks = %+v, %v", checks, err)
	}
}

func TestRecentBackups(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	at := time.Unix(1_700_000_000, 0)
	record := func(job, repository string, success bool) {
		t.Helper()
		at = at.Add(time.Minute)
		run := BackupRun{Job: job, Repository: repository, StartedAt: at, FinishedAt: at, Success: success}
		if !success {
			run.Error = "boom"
		}
		if err := s.RecordBackup(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	for _, success := range []bool{true, true, false, true, true} {
		record("appdb", "s3", success)
	}
	record("appdb", "local", false)
	record("files", "local", true)

	recent, err := s.RecentBackups(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, run := range recent {
		outcome := "ok"
		if !run.Success {
			outcome = "fail"
		}
		summary = append(summary, run.Job+"/"+run.Repository+":"+outcome)
	}
	want := []string{
		"appdb/local:fail",
		"appdb/s3:fail", "appdb/s3:ok", "appdb/s3:ok", // the three newest, oldest first
		"files/local:ok",
	}
	if len(summary) != len(want) {
		t.Fatalf("got %v, want %v", summary, want)
	}
	for i := range want {
		if summary[i] != want[i] {
			t.Fatalf("got %v, want %v", summary, want)
		}
	}

	if none, err := openTemp(t).RecentBackups(ctx, 3); err != nil || len(none) != 0 {
		t.Errorf("empty store: %v, %v", none, err)
	}
}

func TestBackupRunOutcomeAndSummary(t *testing.T) {
	for name, tc := range map[string]struct {
		run                      BackupRun
		wantOutcome, wantSummary string
	}{
		"success":              {BackupRun{Success: true}, "ok", "ok"},
		"success with warning": {BackupRun{Success: true, Error: "retention failed: locked"}, "warning", "ok (warning: retention failed: locked)"},
		"failure":              {BackupRun{Error: "disk on fire"}, "failed", "FAILED: disk on fire"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.run.Outcome(); got != tc.wantOutcome {
				t.Errorf("Outcome = %q, want %q", got, tc.wantOutcome)
			}
			if got := tc.run.Summary(); got != tc.wantSummary {
				t.Errorf("Summary = %q, want %q", got, tc.wantSummary)
			}
		})
	}
}

// A database from before the alert keys were renamed keeps its open alerts under the new keys.
func TestMigrationRenamesAlertKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Put the database back where version 2 left it, with alerts under the old keys.
	old := []string{"repo-check:s3", "backup-stale:appdb", "backup:appdb:s3", "uptime:site", "backup-stale:repo-check:odd"}
	for _, key := range old {
		if _, err := s.db.Exec(`INSERT INTO alerts (key, firing, since, last_notified, message) VALUES (?, 1, 100, 200, 'm')`, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	alerts, err := s.Alerts(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, alert := range alerts {
		got = append(got, alert.Key)
		if !alert.Firing || alert.Since.Unix() != 100 || alert.LastNotified.Unix() != 200 || alert.Message != "m" {
			t.Errorf("alert %q lost its state: %+v", alert.Key, alert)
		}
	}
	want := []string{"backup:appdb:s3", "stale:appdb", "stale:repo-check:odd", "uptime:site", "verify:s3"}
	if !slices.Equal(got, want) {
		t.Errorf("keys after migration = %v, want %v", got, want)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 3 {
		t.Errorf("user_version = %d, %v; want 3", version, err)
	}
}

// vfat, some network mounts and files of another user answer chmod with EPERM or ENOTSUP; that must not
// stop the daemon, while any other failure still does.
func TestModeUnsupported(t *testing.T) {
	for err, want := range map[error]bool{
		syscall.EPERM:   true,
		syscall.ENOTSUP: true,
		syscall.EACCES:  false,
		syscall.EIO:     false,
		syscall.ENOENT:  false,
	} {
		if got := modeUnsupported(&os.PathError{Op: "chmod", Path: "x", Err: err}); got != want {
			t.Errorf("modeUnsupported(%v) = %v, want %v", err, got, want)
		}
	}
}
