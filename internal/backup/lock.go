package backup

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
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
