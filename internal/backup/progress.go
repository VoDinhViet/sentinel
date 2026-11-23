package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"sentinel/internal/restic"
)

// The phases of a running backup.
const (
	PhaseBackup    = "backup"    // restic is reading and storing the data
	PhaseRetention = "retention" // old snapshots are being forgotten and pruned
)

// Progress is how far a running backup has got. It is written to a file while the backup runs,
// so another process (the manager, `sentinel status`) can show it.
type Progress struct {
	Job, Repository string
	PID             int
	Phase           string
	StartedAt       time.Time
	UpdatedAt       time.Time // refreshed every few seconds, even when nothing new happened
	LastDataAt      time.Time // the last time the amount of data moved
	BytesDone       int64
	// BytesTotal is how much there is to do: what restic knows for files, otherwise (a
	// database dump, whose size restic cannot know) the size of the last successful backup.
	// 0 when neither is known.
	BytesTotal int64
	FilesDone  int64
	FilesTotal int64
	// Percent is 0 to 100, or -1 when there is nothing to measure against. Until the backup
	// is done it never passes 99.
	Percent float64
	// Estimated says Percent and BytesTotal come from the last backup, not from restic.
	Estimated bool
	Rate      float64       // bytes per second over the last few minutes
	Remaining time.Duration // 0 when unknown
	Timeout   time.Duration // how long the job may run in all
}

// Stopped is the Error of a backup the user stopped on purpose; it is not a failure to alert about.
const Stopped = "stopped by the user"

// stalledAfter is how long without new data makes a running backup look stuck.
const stalledAfter = 10 * time.Minute

// Stalled says no data has moved for a long time: the database or the network may be stuck.
func (p Progress) Stalled(now time.Time) bool {
	return p.Phase == PhaseBackup && !p.LastDataAt.IsZero() && now.Sub(p.LastDataAt) >= stalledAfter
}

// WillExceedTimeout says the backup is expected to end after the job's timeout.
func (p Progress) WillExceedTimeout() bool {
	return p.Timeout > 0 && p.Remaining > 0 && p.UpdatedAt.Sub(p.StartedAt)+p.Remaining > p.Timeout
}

// Summary is one line for a terminal: "~37% (7.4 GiB of ~20 GiB) 45 MiB/s, ~25m left".
func (p Progress) Summary() string {
	if p.Phase == PhaseRetention {
		return "applying retention (forgetting old snapshots)"
	}
	var parts []string
	switch {
	case p.Percent >= 0 && p.Estimated:
		parts = append(parts, fmt.Sprintf("~%.0f%% (%s of ~%s)", p.Percent, HumanBytes(p.BytesDone), HumanBytes(p.BytesTotal)))
	case p.Percent >= 0:
		parts = append(parts, fmt.Sprintf("%.0f%% (%s of %s)", p.Percent, HumanBytes(p.BytesDone), HumanBytes(p.BytesTotal)))
	default:
		parts = append(parts, HumanBytes(p.BytesDone)+" so far")
	}
	if p.Rate > 0 {
		parts = append(parts, HumanBytes(int64(p.Rate))+"/s")
	}
	if p.Remaining > 0 {
		parts = append(parts, "~"+HumanDuration(p.Remaining)+" left")
	}
	return strings.Join(parts, ", ")
}

// HumanBytes writes a size like 7.4 GiB.
func HumanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value, suffix := float64(size), 0
	for value >= unit && suffix < 5 {
		value /= unit
		suffix++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGTP"[suffix-1])
}

// HumanDuration writes a time like 25m or 3h10m.
func HumanDuration(dur time.Duration) string {
	dur = dur.Round(time.Minute)
	switch {
	case dur < time.Minute:
		return "<1m"
	case dur < time.Hour:
		return fmt.Sprintf("%dm", int(dur.Minutes()))
	}
	hours := int(dur.Hours())
	if minutes := int(dur.Minutes()) % 60; minutes > 0 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}
	return fmt.Sprintf("%dh", hours)
}

// ProgressDir is where running backups write their progress, next to the state database.
func ProgressDir(storePath string) string { return filepath.Join(filepath.Dir(storePath), "progress") }

func progressFile(dir, job string) string { return filepath.Join(dir, safeFileName(job)+".json") }
func stopFile(dir, job string) string     { return filepath.Join(dir, safeFileName(job)+".stop") }

// RequestStop asks whichever process is running the job's backup (the daemon, a
// `sentinel backup run`, a detached run) to stop it, by leaving a request file that process
// looks for every second. A signal to the process would do harm when it is the daemon, which
// runs backups itself. It is an error when the job is not running.
func RequestStop(dir, job string, now time.Time) error {
	if !slices.ContainsFunc(ReadRunning(dir, now), func(p Progress) bool { return p.Job == job }) {
		return fmt.Errorf("no backup of %q is running", job)
	}
	return os.WriteFile(stopFile(dir, job), nil, 0o600)
}

// safeFileName keeps a job name from reaching outside the directory.
func safeFileName(name string) string {
	return strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(name)
}

const (
	progressWriteEvery = 2 * time.Second // at most one write for the data moving
	progressHeartbeat  = 5 * time.Second // a write even when nothing moves (retention takes long and says nothing)
	progressStaleAfter = 2 * time.Minute // a file not refreshed for this long is not a running backup
	rateWindow         = 5 * time.Minute
)

// tracker follows one backup: it turns restic's status lines into Progress, keeps the file for
// other processes up to date and tells the runner's observer.
type tracker struct {
	dir        string // "" for no file
	onProgress func(Progress)
	now        func() time.Time
	estimate   int64 // the size of the last successful backup, 0 if none

	onStop  func() // cancels the backup when the user asks to stop it
	stopped atomic.Bool

	mu        sync.Mutex
	p         Progress
	samples   []rateSample
	lastWrite time.Time
	seen      bool // a status line has come: the first one is shown at once
	stop      chan struct{}
	done      sync.WaitGroup
}

type rateSample struct {
	at    time.Time
	bytes int64
}

func newTracker(dir, job, repository string, estimate int64, timeout time.Duration, now func() time.Time, onProgress func(Progress), onStop func()) *tracker {
	started := now()
	if dir != "" {
		os.Remove(stopFile(dir, job)) // a request left over from an earlier run is not for this one
	}
	t := &tracker{dir: dir, onProgress: onProgress, now: now, estimate: estimate, onStop: onStop, stop: make(chan struct{}),
		p: Progress{Job: job, Repository: repository, PID: os.Getpid(), Phase: PhaseBackup, StartedAt: started, UpdatedAt: started, LastDataAt: started, Percent: -1, Timeout: timeout}}
	t.write()
	t.done.Add(1)
	go t.heartbeat()
	return t
}

// heartbeat keeps the file fresh and, every second, looks for a request to stop the backup.
func (t *tracker) heartbeat() {
	defer t.done.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			t.checkStop()
			if time.Duration(tick)*time.Second%progressHeartbeat == 0 {
				t.mu.Lock()
				t.write()
				t.mu.Unlock()
			}
		}
	}
}

// checkStop cancels the backup when a stop was asked for.
func (t *tracker) checkStop() {
	if t.dir == "" || t.onStop == nil {
		return
	}
	name := stopFile(t.dir, t.p.Job)
	if _, err := os.Stat(name); err != nil {
		return
	}
	os.Remove(name)
	t.stopped.Store(true)
	slog.Warn("backup stopped on request", "job", t.p.Job)
	t.onStop()
}

// status takes restic's latest progress line.
func (t *tracker) status(status restic.Status) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.update(status)
	if !t.seen || t.now().Sub(t.lastWrite) >= progressWriteEvery {
		t.seen = true
		t.write()
		if t.onProgress != nil {
			t.onProgress(t.p)
		}
	}
}

// update recomputes the progress from a status line.
func (t *tracker) update(status restic.Status) {
	now := t.now()
	p := &t.p
	if status.BytesDone > p.BytesDone {
		p.LastDataAt = now
	}
	p.BytesDone, p.FilesDone, p.FilesTotal = status.BytesDone, status.FilesDone, status.TotalFiles
	switch {
	case status.TotalBytes > 0: // restic knows the total (files)
		p.BytesTotal, p.Estimated = status.TotalBytes, false
		p.Percent = min(float64(status.BytesDone)/float64(status.TotalBytes)*100, 99)
	case t.estimate > 0: // a dump: go by the size of the last one
		p.BytesTotal, p.Estimated = t.estimate, true
		p.Percent = min(float64(status.BytesDone)/float64(t.estimate)*100, 99)
	default:
		p.BytesTotal, p.Estimated, p.Percent = 0, false, -1
	}
	t.samples = append(t.samples, rateSample{at: now, bytes: status.BytesDone})
	for len(t.samples) > 1 && now.Sub(t.samples[0].at) > rateWindow {
		t.samples = t.samples[1:]
	}
	p.Rate = 0
	if first := t.samples[0]; now.Sub(first.at) >= 4*time.Second && status.BytesDone > first.bytes {
		p.Rate = float64(status.BytesDone-first.bytes) / now.Sub(first.at).Seconds()
	} else if elapsed := now.Sub(p.StartedAt); elapsed >= 4*time.Second && status.BytesDone > 0 {
		p.Rate = float64(status.BytesDone) / elapsed.Seconds()
	}
	p.Remaining = 0
	if p.Rate > 0 && p.BytesTotal > p.BytesDone {
		p.Remaining = time.Duration(float64(p.BytesTotal-p.BytesDone) / p.Rate * float64(time.Second))
	}
	p.UpdatedAt = now
}

// phase moves the backup to another phase and writes it at once.
func (t *tracker) phase(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.p.Phase, t.p.Percent, t.p.Rate, t.p.Remaining = name, -1, 0, 0
	t.write()
	if t.onProgress != nil {
		t.onProgress(t.p)
	}
}

// write saves the progress file; it must be called with the lock held. A failure to write
// only costs the other processes their view, never the backup.
func (t *tracker) write() {
	t.p.UpdatedAt = t.now()
	t.lastWrite = t.p.UpdatedAt
	if t.dir == "" {
		return
	}
	raw, err := json.Marshal(t.p)
	if err != nil || os.MkdirAll(t.dir, 0o700) != nil {
		return
	}
	target := progressFile(t.dir, t.p.Job)
	temp := fmt.Sprintf("%s.tmp%d", target, os.Getpid())
	if os.WriteFile(temp, raw, 0o600) != nil {
		return
	}
	if os.Rename(temp, target) != nil {
		os.Remove(temp)
	}
}

// finish stops the heartbeat and removes the file: the backup is no longer running.
func (t *tracker) finish() {
	close(t.stop)
	t.done.Wait()
	if t.dir != "" {
		os.Remove(progressFile(t.dir, t.p.Job))
		os.Remove(stopFile(t.dir, t.p.Job))
	}
}

// ReadRunning lists the backups that are running now, from the progress files in dir. A file
// whose process is gone, or that has not been refreshed for two minutes, is not one.
func ReadRunning(dir string, now time.Time) []Progress {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var running []Progress
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var p Progress
		if json.Unmarshal(raw, &p) != nil || p.Job == "" || !processAlive(p.PID) || now.Sub(p.UpdatedAt) > progressStaleAfter {
			continue
		}
		running = append(running, p)
	}
	slices.SortFunc(running, func(a, b Progress) int { return strings.Compare(a.Job, b.Job) })
	return running
}

// processAlive reports whether a process with that id exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
