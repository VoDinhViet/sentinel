package backup

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/restic"
	"sentinel/internal/store"
)

// clock is a time that only moves when the test says so.
type clock struct{ now time.Time }

func (c *clock) read() time.Time { return c.now }

func newTestTracker(t *testing.T, estimate int64, c *clock) *tracker {
	t.Helper()
	tr := newTracker("", "shop", "local", estimate, 6*time.Hour, c.read, nil)
	t.Cleanup(tr.finish)
	return tr
}

func TestProgressOfADumpIsEstimatedFromTheLastBackupAndNeverReaches100(t *testing.T) {
	c := &clock{now: time.Unix(1_000_000, 0)}
	tr := newTestTracker(t, 20<<30, c)
	c.now = c.now.Add(10 * time.Second)
	tr.mu.Lock()
	tr.update(restic.Status{BytesDone: 1 << 30}) // 1 GiB in 10 s
	got := tr.p
	tr.mu.Unlock()
	if !got.Estimated || got.BytesTotal != 20<<30 || got.Percent < 4.9 || got.Percent > 5.1 {
		t.Errorf("progress = %+v", got)
	}
	if got.Rate < float64(100<<20) || got.Rate > float64(110<<20) {
		t.Errorf("rate = %.0f B/s, want about 102 MiB/s", got.Rate)
	}
	// 19 GiB left at ~102 MiB/s is about 3 minutes.
	if got.Remaining < 3*time.Minute || got.Remaining > 3*time.Minute+30*time.Second {
		t.Errorf("remaining = %s", got.Remaining)
	}
	c.now = c.now.Add(10 * time.Second)
	tr.mu.Lock()
	tr.update(restic.Status{BytesDone: 25 << 30}) // the database grew: more than the last dump
	got = tr.p
	tr.mu.Unlock()
	if got.Percent != 99 || got.Remaining != 0 {
		t.Errorf("past the estimate it waits at 99%% with no remaining time: %+v", got)
	}
	if !strings.HasPrefix(got.Summary(), "~99%") {
		t.Errorf("summary = %q", got.Summary())
	}
}

func TestProgressWithoutALastBackupHasNoPercent(t *testing.T) {
	c := &clock{now: time.Unix(1_000_000, 0)}
	tr := newTestTracker(t, 0, c)
	c.now = c.now.Add(8 * time.Second)
	tr.mu.Lock()
	tr.update(restic.Status{BytesDone: 800 << 20, PercentDone: 0}) // restic says 0%: it does not know the total
	got := tr.p
	tr.mu.Unlock()
	if got.Percent != -1 || got.BytesTotal != 0 || got.Remaining != 0 || got.Rate == 0 {
		t.Errorf("progress = %+v", got)
	}
	if want := "800.0 MiB so far, 100.0 MiB/s"; got.Summary() != want {
		t.Errorf("summary = %q, want %q", got.Summary(), want)
	}
}

func TestProgressOfFilesTrustsResticsTotal(t *testing.T) {
	c := &clock{now: time.Unix(1_000_000, 0)}
	tr := newTestTracker(t, 99<<30, c) // an estimate is only for dumps
	c.now = c.now.Add(10 * time.Second)
	tr.mu.Lock()
	tr.update(restic.Status{BytesDone: 300, TotalBytes: 1200, FilesDone: 3, TotalFiles: 12})
	got := tr.p
	tr.mu.Unlock()
	if got.Estimated || got.BytesTotal != 1200 || got.Percent != 25 || got.FilesDone != 3 || got.FilesTotal != 12 {
		t.Errorf("progress = %+v", got)
	}
}

func TestProgressNoticesStallsAndATimeoutThatWillBeMissed(t *testing.T) {
	c := &clock{now: time.Unix(1_000_000, 0)}
	tr := newTestTracker(t, 20<<30, c)
	c.now = c.now.Add(10 * time.Second)
	tr.mu.Lock()
	tr.update(restic.Status{BytesDone: 1 << 20}) // 1 MiB in 10 s: 20 GiB would take days
	got := tr.p
	tr.mu.Unlock()
	if !got.WillExceedTimeout() {
		t.Errorf("at this speed the 6h timeout is missed: %+v", got)
	}
	if got.Stalled(c.now.Add(9 * time.Minute)) {
		t.Error("nine minutes is not stalled")
	}
	if !got.Stalled(c.now.Add(11 * time.Minute)) {
		t.Error("eleven minutes without data is stalled")
	}
}

func TestProgressFileIsWrittenReadAndRemoved(t *testing.T) {
	dir := t.TempDir()
	tr := newTracker(dir, "site/with slash", "local", 0, time.Hour, time.Now, nil)
	tr.status(restic.Status{BytesDone: 123})
	running := ReadRunning(dir, time.Now())
	if len(running) != 1 || running[0].Job != "site/with slash" || running[0].PID != os.Getpid() || running[0].Phase != PhaseBackup {
		t.Fatalf("running = %+v", running)
	}
	tr.phase(PhaseRetention)
	if running = ReadRunning(dir, time.Now()); len(running) != 1 || running[0].Phase != PhaseRetention {
		t.Errorf("phase = %+v", running)
	}
	tr.finish()
	if running = ReadRunning(dir, time.Now()); len(running) != 0 {
		t.Errorf("a finished backup is not running: %+v", running)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("files left behind: %v", left)
	}
}

func TestReadRunningIgnoresDeadAndStaleBackups(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	fresh := now.Format(time.RFC3339Nano)
	old := now.Add(-5 * time.Minute).Format(time.RFC3339Nano)
	pid := os.Getpid()
	write("alive.json", `{"Job":"alive","PID":`+itoa(pid)+`,"UpdatedAt":"`+fresh+`"}`)
	write("dead.json", `{"Job":"dead","PID":2147483646,"UpdatedAt":"`+fresh+`"}`)
	write("stale.json", `{"Job":"stale","PID":`+itoa(pid)+`,"UpdatedAt":"`+old+`"}`)
	write("broken.json", `{not json`)
	write("notes.txt", `x`)
	running := ReadRunning(dir, now)
	if len(running) != 1 || running[0].Job != "alive" {
		t.Errorf("running = %+v", running)
	}
	if got := ReadRunning(filepath.Join(dir, "missing"), now); got != nil {
		t.Errorf("no directory, nothing running: %+v", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestRunnerReportsProgressWhileTheBackupRunsAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	script := `#!/bin/sh
echo '{"message_type":"status","seconds_elapsed":1,"bytes_done":1000,"total_bytes":4000,"total_files":4,"files_done":1,"percent_done":0.25}'
sleep 3
echo '{"message_type":"status","seconds_elapsed":4,"bytes_done":3000,"total_bytes":4000,"total_files":4,"files_done":3,"percent_done":0.75}'
echo '{"message_type":"summary","snapshot_id":"abcdef123456","total_bytes_processed":4000}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ServerName:   "h",
		StorePath:    filepath.Join(dir, "state", "state.db"),
		Repositories: map[string]config.Repository{"r": {URL: "/repo", Password: "p"}},
	}
	job := config.Job{Name: "site", Type: config.JobFiles, Paths: []string{"/x"}, Timeout: time.Minute, Repositories: []string{"r"}}
	var heard []Progress
	done := make(chan []store.BackupRun, 1)
	runner := &Runner{Config: cfg, Bin: bin, OnProgress: func(p Progress) { heard = append(heard, p) }}
	go func() { done <- runner.Run(context.Background(), &job) }()

	deadline := time.Now().Add(2 * time.Second)
	var running []Progress
	for len(running) == 0 && time.Now().Before(deadline) {
		running = ReadRunning(ProgressDir(cfg.StorePath), time.Now())
		time.Sleep(20 * time.Millisecond)
	}
	if len(running) != 1 || running[0].Job != "site" || running[0].Repository != "r" {
		t.Fatalf("while the backup runs another process sees it: %+v", running)
	}
	runs := <-done
	if !runs[0].Success || runs[0].TotalBytes != 4000 {
		t.Fatalf("run = %+v", runs[0])
	}
	if len(heard) == 0 || heard[0].Percent != 25 || heard[0].Job != "site" {
		t.Errorf("the observer heard %+v", heard)
	}
	if running = ReadRunning(ProgressDir(cfg.StorePath), time.Now()); len(running) != 0 {
		t.Errorf("nothing runs afterwards: %+v", running)
	}
}

func TestLastBackupSizeComesFromTheLatestSuccessOfThatRepository(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	record := func(repository string, finished int64, ok bool, total int64) {
		run := store.BackupRun{Job: "db", Repository: repository, StartedAt: time.Unix(finished-5, 0), FinishedAt: time.Unix(finished, 0), Success: ok, TotalBytes: total}
		if err := st.RecordBackup(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := st.LastBackupSize(ctx, "db", "local"); ok || err != nil {
		t.Fatalf("nothing yet: %v %v", ok, err)
	}
	record("local", 100, true, 10<<30)
	record("local", 200, true, 12<<30)
	record("local", 300, false, 0)
	record("s3", 400, true, 99)
	if size, ok, err := st.LastBackupSize(ctx, "db", "local"); err != nil || !ok || size != 12<<30 {
		t.Errorf("size = %d, %v, %v", size, ok, err)
	}
}
