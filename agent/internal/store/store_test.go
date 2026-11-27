package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"sentinel/pkg/alert"
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

// An older sentinel must not open a database a newer one has migrated: it would run on a schema it does not know.
func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err == nil {
		s.Close()
		t.Fatal("Open accepted a database at schema version 99")
	}
	if !strings.Contains(err.Error(), "newer sentinel") {
		t.Errorf("error = %q, want it to say the database came from a newer sentinel", err)
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

	if err := s.SaveAlert(ctx, alert.Alert{Key: "uptime:web", Firing: true, Since: now, LastNotified: now, Message: "down"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAlert(ctx, alert.Alert{Key: "cpu", Firing: false, Since: now, LastNotified: now, Message: "ok"}); err != nil {
		t.Fatal(err)
	}
	// Upsert flips the first alert to resolved.
	if err := s.SaveAlert(ctx, alert.Alert{Key: "uptime:web", Firing: false, Since: now.Add(time.Minute), LastNotified: now, Message: "up"}); err != nil {
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

func TestAlertKeepsItsPendingMessage(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	since := time.Unix(100, 0)
	waiting := alert.Alert{Key: "disk:/", Firing: true, Since: since, LastNotified: since, Message: "disk full",
		Pending: &alert.Pending{Level: 2, Title: "disk full", Body: "95% used"}}
	if err := s.SaveAlert(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAlert(ctx, alert.Alert{Key: "uptime:web", Since: since, LastNotified: since}); err != nil {
		t.Fatal(err)
	}
	alerts, err := s.Alerts(ctx, false)
	if err != nil || len(alerts) != 2 {
		t.Fatalf("alerts = %+v, %v", alerts, err)
	}
	if got := alerts[0].Pending; got == nil || *got != *waiting.Pending {
		t.Errorf("disk:/ pending = %+v, want %+v", got, waiting.Pending)
	}
	if alerts[1].Pending != nil {
		t.Errorf("uptime:web pending = %+v, want none", alerts[1].Pending)
	}

	waiting.Pending = nil
	if err := s.SaveAlert(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	alerts, _ = s.Alerts(ctx, false)
	if alerts[0].Pending != nil {
		t.Errorf("pending = %+v after it was cleared", alerts[0].Pending)
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
	for _, column := range []string{"pending_level", "pending_title", "pending_body"} { // added after version 3
		if _, err := s.db.Exec(`ALTER TABLE alerts DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
	dropIndexesAfterVersion5(t, s)
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
	for _, migrated := range alerts {
		got = append(got, migrated.Key)
		if !migrated.Firing || migrated.Since.Unix() != 100 || migrated.LastNotified.Unix() != 200 || migrated.Message != "m" {
			t.Errorf("alert %q lost its state: %+v", migrated.Key, migrated)
		}
	}
	want := []string{"backup:appdb:s3", "stale:appdb", "stale:repo-check:odd", "uptime:site", "verify:s3"}
	if !slices.Equal(got, want) {
		t.Errorf("keys after migration = %v, want %v", got, want)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != len(migrations) {
		t.Errorf("user_version = %d, %v; want %d", version, err, len(migrations))
	}
}

func TestRecentChecks(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	at := time.Unix(1_700_000_000, 0)
	record := func(name string, ok bool, latency time.Duration) {
		t.Helper()
		at = at.Add(time.Minute)
		if err := s.RecordCheck(ctx, CheckResult{Name: name, At: at, OK: ok, Latency: latency}); err != nil {
			t.Fatal(err)
		}
	}
	for i, ok := range []bool{true, true, false, true} {
		record("web", ok, time.Duration(i+1)*10*time.Millisecond)
	}
	record("api", true, 5*time.Millisecond)

	recent, err := s.RecentChecks(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, result := range recent {
		outcome := "up"
		if !result.OK {
			outcome = "down"
		}
		got = append(got, fmt.Sprintf("%s:%s:%dms", result.Name, outcome, result.Latency.Milliseconds()))
	}
	want := []string{"api:up:5ms", "web:up:20ms", "web:down:30ms", "web:up:40ms"} // the three newest of web, oldest first
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
	if none, err := openTemp(t).RecentChecks(ctx, 3); err != nil || len(none) != 0 {
		t.Errorf("empty store: %v, %v", none, err)
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

// The daemon after a restart and `sentinel status` can open an old database at the same moment: each
// migration must be applied once, not fail the second opener with "duplicate column".
func TestOpenOldDatabaseFromManyProcessesAtOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"pending_level", "pending_title", "pending_body"} { // back to version 3
		if _, err := s.db.Exec(`ALTER TABLE alerts DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
	dropIndexesAfterVersion5(t, s)
	if _, err := s.db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	const openers = 8
	errs := make(chan error, openers)
	start := make(chan struct{})
	for range openers {
		go func() {
			<-start
			opened, err := Open(path)
			if err == nil {
				err = opened.Close()
			}
			errs <- err
		}()
	}
	close(start)
	for range openers {
		if err := <-errs; err != nil {
			t.Errorf("open: %v", err)
		}
	}
}

// dropIndexesAfterVersion5 puts a database back to before the indexes of the status queries, so a
// test that sets user_version back finds the migrations the way an old database has them.
func dropIndexesAfterVersion5(t *testing.T, s *Store) {
	t.Helper()
	for _, index := range []string{"check_results_name_id", "backup_runs_job_repository_id"} {
		if _, err := s.db.Exec(`DROP INDEX ` + index); err != nil {
			t.Fatal(err)
		}
	}
}

// The daemon stopped watching other servers: the alerts it had open for them must not stay open for ever.
func TestMigrationDropsTheAlertsOfTheFleetWatcher(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"fleet:web-01", "fleet:config", "fleet-file", "fleetwork:x", "uptime:fleet:site", "backup:appdb:s3"} {
		if _, err := s.db.Exec(`INSERT INTO alerts (key, firing, since, last_notified, message) VALUES (?, 1, 100, 200, 'm')`, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)-1)); err != nil { // the version before the last migration
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
	var kept []string
	for _, left := range alerts {
		kept = append(kept, left.Key)
	}
	slices.Sort(kept)
	if want := []string{"backup:appdb:s3", "fleetwork:x", "uptime:fleet:site"}; !slices.Equal(kept, want) {
		t.Errorf("alerts left = %v, want %v", kept, want)
	}
}
