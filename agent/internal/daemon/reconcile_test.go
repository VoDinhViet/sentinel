package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sentinel/agent/internal/alert"
	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/store"
)

// reconcileDaemon is a daemon for the one job "site" into the repository "s3"; the runs the tests record
// stand for backups that another process ran.
func reconcileDaemon(t *testing.T) (*Daemon, *recorder) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		ServerName:   "h",
		Repositories: map[string]config.Repository{"s3": {URL: "/r", Password: "p"}},
		Jobs:         []config.Job{{Name: "site", Type: config.JobFiles, Paths: []string{"/x"}, Timeout: time.Minute, Repositories: []string{"s3"}}},
	}
	rec := &recorder{}
	return &Daemon{
		Config: cfg, Store: st,
		Alerts: &alert.Manager{Notifier: rec, Store: st, Server: "h", Cooldown: time.Hour},
		Runner: &backup.Runner{Config: cfg, Store: st},
	}, rec
}

func recordRun(t *testing.T, d *Daemon, run store.BackupRun) {
	t.Helper()
	run.StartedAt, run.FinishedAt = time.Unix(1_700_000_000, 0), time.Unix(1_700_000_060, 0)
	if err := d.Store.RecordBackup(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileReportsABackupRunByHand(t *testing.T) {
	ctx := context.Background()
	d, rec := reconcileDaemon(t)

	recordRun(t, d, store.BackupRun{Job: "site", Repository: "s3", Error: "Fatal: disk on fire"})
	d.ReconcileBackups(ctx)
	if got := rec.titles(); len(got) != 1 || got[0] != "CRITICAL Backup site to s3 failed" {
		t.Fatalf("a failed run by hand must reach the alerts, got %v", got)
	}
	if !d.Alerts.Firing(alert.BackupKey("site", "s3")) {
		t.Error("the alert is not open")
	}

	d.ReconcileBackups(ctx)
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("a run that is already reported must stay quiet, got %v", got)
	}

	recordRun(t, d, store.BackupRun{Job: "site", Repository: "s3", Success: true, SnapshotID: "abcdef123456"})
	d.ReconcileBackups(ctx)
	if got := rec.titles(); len(got) != 1 || got[0] != "RESOLVED Backup site to s3 succeeded" {
		t.Fatalf("a good run by hand must close the open alert, got %v", got)
	}
	d.ReconcileBackups(ctx)
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("got %v after the alert was closed", got)
	}
}

func TestReconcileReportsFailedRetentionOfARunByHand(t *testing.T) {
	d, rec := reconcileDaemon(t)
	recordRun(t, d, store.BackupRun{Job: "site", Repository: "s3", Success: true, SnapshotID: "abcdef123456", Error: "retention failed: locked"})
	d.ReconcileBackups(context.Background())
	if got := rec.titles(); len(got) != 1 || got[0] != "WARNING Retention for site in s3 failed" {
		t.Fatalf("got %v", got)
	}
	d.ReconcileBackups(context.Background())
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("a retention failure already reported must stay quiet, got %v", got)
	}
}

func TestReconcileLeavesOutWhatIsNotToBeReported(t *testing.T) {
	ctx := context.Background()
	d, rec := reconcileDaemon(t)
	recordRun(t, d, store.BackupRun{Job: "site", Repository: "s3", Error: backup.Stopped})
	recordRun(t, d, store.BackupRun{Job: "removed-job", Repository: "s3", Error: "boom"})
	recordRun(t, d, store.BackupRun{Job: "site", Repository: "old-repo", Error: "boom"})
	d.ReconcileBackups(ctx)
	if got := rec.titles(); len(got) != 0 {
		t.Errorf("a stopped run, a removed job and a removed repository say nothing, got %v", got)
	}

	// A job the daemon is running now reports its own outcome when it ends.
	recordRun(t, d, store.BackupRun{Job: "site", Repository: "s3", Error: "boom"})
	lock := new(sync.Mutex)
	lock.Lock()
	d.running.Store("site", lock)
	d.ReconcileBackups(ctx)
	if got := rec.titles(); len(got) != 0 {
		t.Errorf("a job running in the daemon must be left to RunBackup, got %v", got)
	}
	lock.Unlock()
	d.ReconcileBackups(ctx)
	if got := rec.titles(); len(got) != 1 {
		t.Errorf("once it is not running the run is reported, got %v", got)
	}
}

func TestReconcileIsAMinuteTask(t *testing.T) {
	for _, task := range (&Daemon{}).builtinTasks() {
		if task.Name == "reconcile-backups" {
			if task.Every != time.Minute || !task.AtStart {
				t.Errorf("task = %+v", task)
			}
			return
		}
	}
	t.Error("no reconcile-backups task")
}
