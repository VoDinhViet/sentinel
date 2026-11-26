package backup

import (
	"path/filepath"
	"testing"

	"sentinel/internal/config"
)

// Two daemons on one state directory would both run the uptime checks and send their own alerts, and
// both start `restic check` on the same repository, so the loser reports a false "repository check failed".
func TestLockDaemonLetsOnlyOneProcessHoldIt(t *testing.T) {
	runner := &Runner{Config: &config.Config{StorePath: filepath.Join(t.TempDir(), "state.db")}}

	held, release, err := runner.LockDaemon()
	if err != nil || !held {
		t.Fatalf("first lock: held=%v err=%v", held, err)
	}
	if held, _, err := runner.LockDaemon(); err != nil || held {
		t.Errorf("second lock while the first is held: held=%v err=%v, want it refused", held, err)
	}
	release()
	held, release, err = runner.LockDaemon()
	if err != nil || !held {
		t.Errorf("lock after release: held=%v err=%v, want it granted", held, err)
	}
	release()
}

func TestLockDaemonDoesNotBlockJobLocks(t *testing.T) {
	runner := &Runner{Config: &config.Config{StorePath: filepath.Join(t.TempDir(), "state.db")}}
	_, releaseDaemon, _ := runner.LockDaemon()
	defer releaseDaemon()
	held, releaseJob, err := runner.lockJob("db")
	defer releaseJob()
	if err != nil || !held {
		t.Errorf("job lock while the daemon lock is held: held=%v err=%v", held, err)
	}
}

func TestLockDaemonWithoutAStateDirectoryHoldsNothing(t *testing.T) {
	held, release, err := (&Runner{Config: &config.Config{}}).LockDaemon()
	defer release()
	if err != nil || !held {
		t.Errorf("held=%v err=%v, want nothing to lock in tests without a state directory", held, err)
	}
}

func TestDaemonRunningSeesTheHolderOfTheLock(t *testing.T) {
	runner := &Runner{Config: &config.Config{StorePath: filepath.Join(t.TempDir(), "state.db")}}
	if running, err := runner.DaemonRunning(); err != nil || running {
		t.Fatalf("before any daemon: running=%v err=%v", running, err)
	}
	_, release, err := runner.LockDaemon()
	if err != nil {
		t.Fatal(err)
	}
	if running, err := runner.DaemonRunning(); err != nil || !running {
		t.Errorf("while a daemon holds the lock: running=%v err=%v", running, err)
	}
	release()
	if running, err := runner.DaemonRunning(); err != nil || running {
		t.Errorf("after it stopped: running=%v err=%v", running, err)
	}
	// Asking must not take the lock away from a daemon that starts afterwards.
	if held, release, err := runner.LockDaemon(); err != nil || !held {
		t.Errorf("daemon lock after asking: held=%v err=%v", held, err)
	} else {
		release()
	}
}
