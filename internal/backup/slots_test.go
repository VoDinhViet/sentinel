package backup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sentinel/internal/config"
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
