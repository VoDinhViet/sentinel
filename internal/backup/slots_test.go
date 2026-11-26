package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/store"
)

func slotRunner(t *testing.T, maxParallel int) *Runner {
	t.Helper()
	return &Runner{Config: &config.Config{StorePath: filepath.Join(t.TempDir(), "state.db"), Resources: config.Resources{MaxParallel: maxParallel}}}
}

func TestSlotsLimitHowManyBackupsRun(t *testing.T) {
	runner := slotRunner(t, 2)
	ctx := context.Background()
	first, err := runner.acquireSlot(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.acquireSlot(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := runner.acquireSlot(waiting, "c"); err == nil {
		t.Fatal("a third backup must wait when max_parallel is 2")
	}
	first()
	third, err := runner.acquireSlot(ctx, "c")
	if err != nil {
		t.Fatalf("a freed slot must be taken: %v", err)
	}
	second()
	third()
}

func TestSlotWaitEndsWhenAnotherBackupFinishes(t *testing.T) {
	runner := slotRunner(t, 1)
	first, err := runner.acquireSlot(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		release, err := runner.acquireSlot(context.Background(), "b")
		release()
		got <- err
	}()
	time.AfterFunc(200*time.Millisecond, first)
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(slotPoll + 3*time.Second):
		t.Fatal("the waiting backup never got the slot")
	}
}

func TestNoStateDirectoryMeansNoSlots(t *testing.T) {
	release, err := (&Runner{Config: &config.Config{}}).acquireSlot(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestJobWaitingForARepositoryLockGivesItsSlotToAnotherJob(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	// Locked on the first call, succeeds on the second.
	script := "#!/bin/sh\n" +
		"if [ ! -f " + dir + "/called ]; then touch " + dir + "/called\n" +
		"  echo '{\"message_type\":\"exit_error\",\"code\":11,\"message\":\"locked\"}'; exit 11; fi\n" +
		"echo '{\"message_type\":\"summary\",\"snapshot_id\":\"abc\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		Config: &config.Config{
			ServerName:   "h",
			StorePath:    filepath.Join(dir, "state.db"),
			Resources:    config.Resources{MaxParallel: 1},
			Repositories: map[string]config.Repository{"r": {URL: "/r", Password: "p"}},
		},
		Bin: bin, LockRetries: 1, LockRetryDelay: time.Second,
	}
	job := config.Job{Name: "f", Type: config.JobFiles, Paths: []string{"/x"}, Timeout: time.Minute, Repositories: []string{"r"}}

	finished := make(chan []store.BackupRun, 1)
	go func() { finished <- runner.Run(context.Background(), &job) }()
	for { // the first attempt found the repository locked: the job is now waiting
		if _, err := os.Stat(filepath.Join(dir, "called")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	other, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	release, err := runner.acquireSlot(other, "other")
	if err != nil {
		t.Fatalf("a job waiting for the repository lock must not hold the only slot: %v", err)
	}
	release()

	runs := <-finished
	if len(runs) != 1 || !runs[0].Success {
		t.Fatalf("the waiting job must take a slot again and finish: %+v", runs)
	}
}

func TestRunInterruptedByShutdownIsRecordedAsStoppedNotAsAFailure(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 3600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{ServerName: "h", Repositories: map[string]config.Repository{"r": {URL: "/r", Password: "p"}}}
	job := config.Job{Name: "f", Type: config.JobFiles, Paths: []string{"/x"}, Timeout: time.Hour, Repositories: []string{"r"}}

	ctx, shutdown := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, shutdown)
	runs := (&Runner{Config: cfg, Store: st, Bin: bin}).Run(ctx, &job)
	if len(runs) != 1 || runs[0].Success || runs[0].Error != Stopped {
		t.Fatalf("a run cut short by the shutdown is not a failure to alert about: %+v", runs)
	}
	latest, err := st.LatestBackups(context.Background())
	if err != nil || len(latest) != 1 || latest[0].Error != Stopped {
		t.Errorf("the stored run must say the same, or the daemon would alert about it at its next start: %+v %v", latest, err)
	}
}
