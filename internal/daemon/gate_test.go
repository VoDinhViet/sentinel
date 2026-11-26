package daemon

import (
	"context"
	"testing"
	"time"

	"sentinel/internal/backup"
	"sentinel/internal/config"
)

// settled is long enough for a goroutine that can go on to have done so.
const settled = 50 * time.Millisecond

func TestRepoGateLetsBackupsRunTogether(t *testing.T) {
	var gate repoGate
	ctx := context.Background()
	for range 3 {
		if err := gate.enter(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		gate.leave()
	}
}

func TestRepoGatePruneWaitsForBackupsAndBackupsWaitForPrune(t *testing.T) {
	var gate repoGate
	ctx := context.Background()
	if err := gate.enter(ctx); err != nil {
		t.Fatal(err)
	}

	pruneHeld, laterBackup := make(chan struct{}), make(chan struct{})
	go func() {
		_ = gate.exclusive(ctx)
		close(pruneHeld)
	}()
	time.Sleep(settled) // the prune is waiting for the running backup
	go func() {
		_ = gate.enter(ctx)
		close(laterBackup)
	}()

	select {
	case <-pruneHeld:
		t.Fatal("the prune started while a backup ran")
	case <-laterBackup:
		t.Fatal("a backup started while a prune was waiting")
	case <-time.After(settled):
	}

	gate.leave() // the running backup ends
	select {
	case <-pruneHeld:
	case <-time.After(time.Second):
		t.Fatal("the prune did not start when the backup ended")
	}
	select {
	case <-laterBackup:
		t.Fatal("a backup started during a prune")
	case <-time.After(settled):
	}

	gate.release()
	select {
	case <-laterBackup:
	case <-time.After(time.Second):
		t.Fatal("the backup did not start when the prune ended")
	}
}

func TestRepoGateWaitEndsWithItsContext(t *testing.T) {
	var gate repoGate
	if err := gate.exclusive(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), settled)
	defer cancel()
	if err := gate.enter(ctx); err == nil {
		t.Error("a backup entered during a prune")
	}
	if err := gate.exclusive(ctx); err == nil {
		t.Error("a second prune entered during a prune")
	}
	gate.release()
	// A prune that gave up waiting must not keep the backups out.
	if err := gate.enter(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunBackupWaitsForAPruneOfItsRepository(t *testing.T) {
	d := &Daemon{}
	if err := d.gate("s3").exclusive(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), settled)
	defer cancel()
	runs := d.RunBackup(ctx, &config.Job{Name: "a", Repositories: []string{"s3"}}) // no Runner: it must not get that far
	if len(runs) != 1 || runs[0].Error != backup.Stopped || runs[0].Repository != "s3" {
		t.Errorf("runs = %+v", runs)
	}
}
