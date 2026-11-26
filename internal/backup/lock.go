package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// AlreadyRunning is the Error of the run reported, without being recorded,
// when the job is already running (here or in another sentinel process).
const AlreadyRunning = "backup already running"

// lockJob takes an exclusive advisory lock for the job in a "locks" directory
// next to the state database, so the daemon, `sentinel backup run` and a "backup
// now" from the server never run the same job at once. held is false when
// another process has the lock. release is always safe to call.
func (r *Runner) lockJob(jobName string) (held bool, release func(), err error) {
	return r.lockNamed("job-" + url.PathEscape(jobName) + ".lock")
}

// LockDaemon takes the exclusive lock that lets one daemon (`sentinel run`) work on this state
// directory: a second one would run every uptime check and send its own alerts, and start `restic check`
// on a repository the first is checking. held is false when another process has the lock. release is
// always safe to call.
func (r *Runner) LockDaemon() (held bool, release func(), err error) {
	return r.lockNamed("daemon.lock")
}

// DaemonRunning reports whether a daemon holds the daemon lock of this state directory. It takes the
// lock for an instant to find out, so a daemon starting at that very moment can fail to start and
// be restarted by its service manager; ask rarely (a status command), not in a loop.
func (r *Runner) DaemonRunning() (bool, error) {
	free, release, err := r.lockNamed("daemon.lock")
	if err != nil {
		return false, err
	}
	release()
	return !free, nil
}

// lockNamed takes an exclusive advisory lock on a file of that name in the "locks" directory.
func (r *Runner) lockNamed(fileName string) (held bool, release func(), err error) {
	if r.Config == nil || r.Config.StorePath == "" {
		return true, func() {}, nil // no state directory (tests): nothing to lock
	}
	lockDir := filepath.Join(filepath.Dir(r.Config.StorePath), "locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return false, func() {}, fmt.Errorf("create lock directory: %w", err)
	}
	lockFile, err := os.OpenFile(filepath.Join(lockDir, fileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, func() {}, fmt.Errorf("open lock %s: %w", fileName, err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lockFile.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, func() {}, nil
		}
		return false, func() {}, fmt.Errorf("lock %s: %w", fileName, err)
	}
	// The lock lives as long as the open file; closing it releases the lock.
	return true, func() { lockFile.Close() }, nil
}

// slotPoll is how often a backup that is waiting for a free slot looks again.
const slotPoll = 2 * time.Second

// acquireSlot waits until fewer than Resources.MaxParallel backups are running on this
// machine and takes one of the slots (advisory locks next to the job locks, so every sentinel
// process shares them and a dead process frees its slot). It returns early with the context's
// error when ctx ends first. release is always safe to call.
func (r *Runner) acquireSlot(ctx context.Context, jobName string) (release func(), err error) {
	if r.Config == nil || r.Config.StorePath == "" || r.Config.Resources.MaxParallel < 1 {
		return func() {}, nil
	}
	lockDir := filepath.Join(filepath.Dir(r.Config.StorePath), "locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return func() {}, fmt.Errorf("create lock directory: %w", err)
	}
	waiting := false
	for {
		for slot := range r.Config.Resources.MaxParallel {
			slotFile, err := os.OpenFile(filepath.Join(lockDir, fmt.Sprintf("slot-%d.lock", slot)), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				return func() {}, fmt.Errorf("open slot lock: %w", err)
			}
			err = syscall.Flock(int(slotFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				if waiting {
					slog.Info("got a backup slot", "job", jobName, "slot", slot)
				}
				return func() { slotFile.Close() }, nil
			}
			slotFile.Close()
			if !errors.Is(err, syscall.EWOULDBLOCK) {
				return func() {}, fmt.Errorf("lock slot: %w", err)
			}
		}
		if !waiting {
			waiting = true
			slog.Info("waiting for a free backup slot", "job", jobName, "max_parallel", r.Config.Resources.MaxParallel)
		}
		select {
		case <-ctx.Done():
			return func() {}, ctx.Err()
		case <-time.After(slotPoll):
		}
	}
}
