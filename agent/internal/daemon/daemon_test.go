package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sentinel/agent/internal/alert"
	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/notify"
	"sentinel/agent/internal/store"
)

func TestIsStale(t *testing.T) {
	loc := time.UTC
	daily := &config.Job{Schedule: "0 2 * * *", Timeout: time.Hour}
	weekdays := &config.Job{Schedule: "0 2 * * 1-5", Timeout: time.Hour}
	workHours := &config.Job{Schedule: "*/15 8-18 * * *", Timeout: 10 * time.Minute}
	// Friday 2026-10-09 18:45, the last run of a working day, and the nights and weekend after it.
	fridayEvening := time.Date(2026, 10, 9, 18, 45, 0, 0, loc)
	started := time.Date(2026, 10, 1, 0, 0, 0, 0, loc)
	// Sunday 2026-10-11 03:00.
	sunday := time.Date(2026, 10, 11, 3, 0, 0, 0, loc)
	friday2am := time.Date(2026, 10, 9, 2, 5, 0, 0, loc)

	cases := []struct {
		name  string
		job   *config.Job
		last  time.Time
		now   time.Time
		stale bool
	}{
		{"daily fresh", daily, sunday.Add(-20 * time.Hour), sunday, false},
		{"daily one missed run", daily, sunday.Add(-30 * time.Hour), sunday, false},
		{"daily two missed runs", daily, sunday.Add(-50 * time.Hour), sunday, true},
		{"weekdays over weekend", weekdays, friday2am, sunday, false},
		{"working hours, overnight after the last run", workHours, fridayEvening, fridayEvening.Add(8 * time.Hour), false},
		{"working hours, over the weekend", workHours, fridayEvening, fridayEvening.Add(40 * time.Hour), true},
		{"working hours, second run of the morning still running", workHours, fridayEvening.Add(-24 * time.Hour), fridayEvening.Add(-24 * time.Hour).Add(13*time.Hour + 35*time.Minute), false},
		{"working hours, two runs of the morning missed", workHours, fridayEvening.Add(-24 * time.Hour), fridayEvening.Add(-24 * time.Hour).Add(14 * time.Hour), true},
		{"never ran, just started", daily, time.Time{}, started.Add(time.Hour), false},
		{"never ran, long after start", daily, time.Time{}, started.Add(72 * time.Hour), true},
		{"unscheduled", &config.Job{}, time.Time{}, sunday, false},
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

func (r *recorder) Deliver(_ context.Context, m notify.Message) error {
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
		Jobs: []config.Job{{Name: "site", Type: config.JobFiles, Paths: []string{"/x"},
			Timeout: time.Minute, Repositories: []string{"s3"}}},
		Alert: config.AlertSettings{NotifySuccess: true},
	}
	rec := &recorder{}
	d := &Daemon{
		Config: cfg, Store: st,
		Alerts: &alert.Manager{Notifier: rec, Store: st, Server: "h", Cooldown: time.Hour},
		Runner: &backup.Runner{Config: cfg, Store: st, Bin: bin},
	}
	job := &cfg.Jobs[0]

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
		Config: cfg, Store: st, Alerts: &alert.Manager{}, Runner: &backup.Runner{Config: cfg},
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
		Jobs: []config.Job{{Name: "site", Type: config.JobFiles, Paths: []string{"/x"},
			Timeout: time.Minute, Repositories: []string{"s3"}}},
	}
	rec := &recorder{}
	d := &Daemon{
		Config: cfg, Store: st,
		Alerts: &alert.Manager{Notifier: rec, Store: st, Server: "h", Cooldown: time.Hour},
		Runner: &backup.Runner{Config: cfg, Store: st, Bin: slow},
	}
	job := &cfg.Jobs[0]

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

// shutdownDaemon runs a daemon whose one task runs until it is cancelled or released, stops it
// and says how long Run took to return and whether the task was cancelled. The task ends by itself
// after releaseAfter, and a second stop signal arrives after forceAfter (zero = never).
func shutdownDaemon(t *testing.T, wait, releaseAfter, forceAfter time.Duration) (took time.Duration, cancelled bool) {
	t.Helper()
	cfg := &config.Config{ServerName: "h"}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	started := make(chan struct{})
	release, force := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var wasCancelled atomic.Bool
	dm := &Daemon{
		Config: cfg, Store: st, Alerts: &alert.Manager{}, Runner: &backup.Runner{Config: cfg},
		Tasks: []Task{{Name: "long", Every: time.Second, Run: func(ctx context.Context) {
			once.Do(func() { close(started) })
			select {
			case <-ctx.Done():
				wasCancelled.Store(true)
			case <-release:
			}
		}}},
		ShutdownWait: wait, ForceStop: force, ShutdownTimeout: 5 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dm.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("task never ran")
	}
	begin := time.Now()
	cancel()
	if releaseAfter > 0 {
		time.AfterFunc(releaseAfter, func() { close(release) })
	}
	if forceAfter > 0 {
		time.AfterFunc(forceAfter, func() { close(force) })
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	return time.Since(begin), wasCancelled.Load()
}

func TestRunWaitsForRunningJobsBeforeCancelling(t *testing.T) {
	took, cancelled := shutdownDaemon(t, time.Minute, 300*time.Millisecond, 0)
	if cancelled || took < 250*time.Millisecond {
		t.Errorf("a job that finishes within shutdown_wait must not be cancelled (took %v, cancelled %v)", took, cancelled)
	}
}

func TestRunCancelsJobsThatOutlastShutdownWait(t *testing.T) {
	took, cancelled := shutdownDaemon(t, 400*time.Millisecond, 0, 0)
	if !cancelled || took < 350*time.Millisecond {
		t.Errorf("a job past shutdown_wait must be cancelled after it (took %v, cancelled %v)", took, cancelled)
	}
}

func TestSecondStopSignalCancelsAtOnce(t *testing.T) {
	took, cancelled := shutdownDaemon(t, time.Hour, 0, 300*time.Millisecond)
	if !cancelled || took > 5*time.Second {
		t.Errorf("a second stop must cancel the job without waiting (took %v, cancelled %v)", took, cancelled)
	}
}

func TestAPanicInTheFirstRunOfATaskDoesNotEndTheDaemon(t *testing.T) {
	d := &Daemon{}
	d.runAtStart(context.Background(), Task{Name: "broken", Run: func(context.Context) { panic("boom") }}) // a panic here would fail the test
}
