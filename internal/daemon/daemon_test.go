package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel/internal/alert"
	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/notify"
	"sentinel/internal/store"
)

func TestIsStale(t *testing.T) {
	loc := time.UTC
	daily := &config.Backup{Schedule: "0 2 * * *", Timeout: time.Hour}
	weekdays := &config.Backup{Schedule: "0 2 * * 1-5", Timeout: time.Hour}
	started := time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	// Sunday 2026-10-11 03:00.
	sunday := time.Date(2026, 10, 11, 3, 0, 0, 0, loc)
	friday2am := time.Date(2026, 10, 9, 2, 5, 0, 0, loc)

	cases := []struct {
		name  string
		job   *config.Backup
		last  time.Time
		now   time.Time
		stale bool
	}{
		{"daily fresh", daily, sunday.Add(-20 * time.Hour), sunday, false},
		{"daily one missed run", daily, sunday.Add(-30 * time.Hour), sunday, false},
		{"daily two missed runs", daily, sunday.Add(-50 * time.Hour), sunday, true},
		{"weekdays over weekend", weekdays, friday2am, sunday, false},
		{"never ran, just started", daily, time.Time{}, started.Add(time.Hour), false},
		{"never ran, long after start", daily, time.Time{}, started.Add(72 * time.Hour), true},
		{"unscheduled", &config.Backup{}, time.Time{}, sunday, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stale, _ := isStale(tc.job, tc.last, started, tc.now)
			if stale != tc.stale {
				t.Errorf("stale = %v, want %v", stale, tc.stale)
			}
		})
	}
}

type recorder struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (r *recorder) Send(_ context.Context, m notify.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *recorder) titles() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.msgs {
		out = append(out, m.Level.String()+" "+m.Title)
	}
	r.msgs = nil
	return out
}

// fakeRestic succeeds unless the file "fail" exists in its directory.
func fakeRestic(t *testing.T) (bin, failFlag string) {
	dir := t.TempDir()
	bin, failFlag = filepath.Join(dir, "restic"), filepath.Join(dir, "fail")
	script := "#!/bin/sh\n" +
		"if [ -f " + failFlag + " ]; then echo '{\"message_type\":\"exit_error\",\"code\":1,\"message\":\"Fatal: disk on fire\"}' >&2; exit 1; fi\n" +
		"echo '{\"message_type\":\"summary\",\"snapshot_id\":\"abcdef123456\",\"data_added\":10}'\n"
	os.WriteFile(bin, []byte(script), 0o755)
	return bin, failFlag
}

func TestRunBackupAlerts(t *testing.T) {
	ctx := context.Background()
	bin, failFlag := fakeRestic(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		ServerName:   "h",
		Repositories: map[string]config.Repository{"s3": {URL: "/r", Password: "p"}},
		Backups: []config.Backup{{Name: "site", Type: config.BackupFiles, Paths: []string{"/x"},
			Timeout: time.Minute, Repositories: []string{"s3"}}},
		Alert: config.AlertSettings{NotifySuccess: true},
	}
	rec := &recorder{}
	d := &Daemon{
		Config: cfg, Store: st,
		Alerts:  &alert.Manager{Notifier: rec, Store: st, Server: "h", Cooldown: time.Hour},
		Backups: &backup.Runner{Config: cfg, Store: st, Bin: bin},
	}
	job := &cfg.Backups[0]

	d.RunBackup(ctx, job)
	if got := rec.titles(); len(got) != 1 || got[0] != "INFO Backup site completed" {
		t.Fatalf("success messages = %v", got)
	}

	os.WriteFile(failFlag, nil, 0o644)
	d.RunBackup(ctx, job)
	got := rec.titles()
	if len(got) != 1 || got[0] != "CRITICAL Backup site to s3 failed" {
		t.Fatalf("failure messages = %v", got)
	}
	d.RunBackup(ctx, job) // still failing, within cooldown
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("repeat failure should be silent, got %v", got)
	}

	os.Remove(failFlag)
	d.RunBackup(ctx, job)
	got = rec.titles()
	if len(got) != 2 || got[0] != "RESOLVED Backup site to s3 succeeded" {
		t.Fatalf("recovery messages = %v", got)
	}

	// Repository check uses the same alert flow.
	os.WriteFile(failFlag, nil, 0o644)
	d.CheckRepositories(ctx)
	if got := rec.titles(); len(got) != 1 || !strings.Contains(got[0], "Repository check failed: s3") {
		t.Fatalf("repo check messages = %v", got)
	}
}

func TestRunStopsAndCancelsJobs(t *testing.T) {
	cfg := &config.Config{ServerName: "h"}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	started := make(chan struct{})
	cancelled := make(chan struct{})
	var once sync.Once
	d := &Daemon{
		Config: cfg, Store: st, Alerts: &alert.Manager{}, Backups: &backup.Runner{Config: cfg},
		Tasks: []Task{{Name: "slow", Every: time.Second, Run: func(ctx context.Context) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			close(cancelled)
		}}},
		ShutdownTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("task never ran")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	select {
	case <-cancelled:
	default:
		t.Error("running task was not cancelled")
	}
}

func TestRunBackupSkipsWhenJobAlreadyRunning(t *testing.T) {
	ctx := context.Background()
	slow := filepath.Join(t.TempDir(), "restic")
	os.WriteFile(slow, []byte("#!/bin/sh\nsleep 1\necho '{\"message_type\":\"summary\",\"snapshot_id\":\"abcdef123456\"}'\n"), 0o755)
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{
		ServerName:   "h",
		Repositories: map[string]config.Repository{"s3": {URL: "/r", Password: "p"}},
		Backups: []config.Backup{{Name: "site", Type: config.BackupFiles, Paths: []string{"/x"},
			Timeout: time.Minute, Repositories: []string{"s3"}}},
	}
	rec := &recorder{}
	d := &Daemon{
		Config: cfg, Store: st,
		Alerts:  &alert.Manager{Notifier: rec, Store: st, Server: "h", Cooldown: time.Hour},
		Backups: &backup.Runner{Config: cfg, Store: st, Bin: slow},
	}
	job := &cfg.Backups[0]

	first := make(chan []store.BackupRun, 1)
	go func() { first <- d.RunBackup(ctx, job) }()
	time.Sleep(300 * time.Millisecond)

	second := d.RunBackup(ctx, job)
	if len(second) != 1 || second[0].Error != backup.AlreadyRunning || second[0].Success {
		t.Fatalf("overlapping run = %+v", second)
	}
	if got := rec.titles(); len(got) != 0 {
		t.Errorf("skipping must not alert, got %v", got)
	}
	if runs := <-first; !runs[0].Success {
		t.Fatalf("first run = %+v", runs[0])
	}
	// Once the first finished, the job can run again.
	if runs := d.RunBackup(ctx, job); !runs[0].Success {
		t.Fatalf("run after completion = %+v", runs[0])
	}
}
