package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sentinel/agent/internal/alertkey"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/fleet"
	"sentinel/pkg/alert"
	"sentinel/pkg/notify"
	"sentinel/pkg/termtext"
)

// fleetKeyPrefix starts the alert key of every server of the fleet (see alertkey.Fleet).
const fleetKeyPrefix = "fleet:"

// FleetWatcher asks the servers of a fleet file how they are and alerts when one cannot be reached
// FailAfter rounds in a row. It reports only what those servers cannot report themselves: their own
// alerts (a failed backup, a disk filling) reach their own notifiers, and are not sent again from here.
// An alert closes when the server answers again.
type FleetWatcher struct {
	Alerts    *alert.Manager
	Path      string // the fleet file, read again every round so a server added to it is watched at once
	FailAfter int
	Every     time.Duration // how often Check runs, to say so in the alert
	// Poll asks the servers of the file; fleet.Poll unless a test replaces it.
	Poll func(ctx context.Context, path string) ([]fleet.ServerStatus, error)

	round        sync.Mutex // held for the whole of a Check, so two rounds never count the same failure twice
	mu           sync.Mutex
	failures     map[string]int // rounds in a row each server has failed
	fileErrShown string         // the problem with the file that was last logged, so it is logged once
}

// fleetTask is the interval task of cfg's fleet watcher, or none when the config names no fleet file.
func fleetTask(cfg *config.Config, alerts *alert.Manager) []Task {
	if cfg.Fleet.Config == "" {
		return nil
	}
	watcher := &FleetWatcher{Alerts: alerts, Path: cfg.Fleet.Config, FailAfter: cfg.Fleet.FailAfter, Every: cfg.Fleet.Every}
	return []Task{{Name: "fleet", Every: cfg.Fleet.Every, Run: watcher.Check, AtStart: true}}
}

// Check is one round: read the fleet file, ask every server in it, and open or close the alert of each.
// A round that finds the previous one still running does nothing: it would count the same failure twice.
func (w *FleetWatcher) Check(ctx context.Context) {
	if !w.round.TryLock() {
		slog.Warn("fleet round skipped: the previous one is still running", "every", w.Every)
		return
	}
	defer w.round.Unlock()
	poll := w.Poll
	if poll == nil {
		poll = fleet.Poll
	}
	answers, err := poll(ctx, w.Path)
	if ctx.Err() != nil {
		return // the daemon is stopping: every call failed because of that, not because of the servers
	}
	if err != nil {
		w.fileUnreadable(ctx, err)
		return // the alerts of the servers stay as they were: nothing was learned about them
	}
	w.fileReadable(ctx)
	var keys []string
	for _, answer := range answers {
		w.observe(ctx, answer)
		keys = append(keys, alertkey.Fleet(answer.Server.Name))
	}
	w.forgetServersOtherThan(answers)
	w.Alerts.ResolveMissing(ctx, fleetKeyPrefix, keys, "Server no longer watched")
}

// observe counts the round for one server and raises or resolves its alert.
func (w *FleetWatcher) observe(ctx context.Context, answer fleet.ServerStatus) {
	name, key := answer.Server.Name, alertkey.Fleet(answer.Server.Name)
	failures := w.countFailures(name, answer.Err == nil)
	switch {
	case answer.Err == nil:
		w.Alerts.Resolve(ctx, key, name+" answers again", fmt.Sprintf("%s reports version %s.", answer.Server.URL, versionOrUnknown(answer)))
	case failures >= w.FailAfter:
		w.Alerts.Fire(ctx, key, notify.Critical, name+" cannot be reached",
			fmt.Sprintf("%s.\n%d rounds in a row failed (one every %s), asking %s.", failureText(answer.Err), failures, w.Every, answer.Server.URL))
	default:
		slog.Warn("fleet server did not answer", "server", name, "failures", failures, "alert_after", w.FailAfter, "error", answer.Err)
	}
}

// maxFailureRunes is the most of a failure that goes in an alert.
const maxFailureRunes = 300

// failureText is why a server failed, for an alert. It can quote what the server or its certificate
// said, so it is cleaned of control characters and cut.
func failureText(err error) string {
	return termtext.Shorten(termtext.Clean(err.Error()), maxFailureRunes)
}

func versionOrUnknown(answer fleet.ServerStatus) string {
	if answer.Report.Version == "" {
		return "unknown"
	}
	return answer.Report.Version
}

// countFailures returns how many rounds in a row the server has failed, counting this one.
func (w *FleetWatcher) countFailures(name string, ok bool) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failures == nil {
		w.failures = map[string]int{}
	}
	if ok {
		w.failures[name] = 0
	} else {
		w.failures[name]++
	}
	return w.failures[name]
}

// forgetServersOtherThan drops the counts of servers that have left the file, so one that comes back
// starts from zero.
func (w *FleetWatcher) forgetServersOtherThan(answers []fleet.ServerStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	listed := map[string]bool{}
	for _, answer := range answers {
		listed[answer.Server.Name] = true
	}
	for name := range w.failures {
		if !listed[name] {
			delete(w.failures, name)
		}
	}
}

// fileUnreadable alerts that nothing is being watched: a daemon that silently stops watching is worse
// than one that never did. The cause is logged once, not every round.
func (w *FleetWatcher) fileUnreadable(ctx context.Context, err error) {
	w.mu.Lock()
	repeated := w.fileErrShown == err.Error()
	w.fileErrShown = err.Error()
	w.mu.Unlock()
	if !repeated {
		slog.Error("cannot read the fleet file", "path", w.Path, "error", err)
	}
	w.Alerts.Fire(ctx, alertkey.FleetFile, notify.Warning, "The fleet file cannot be read: no server is being watched",
		fmt.Sprintf("%s: %v", w.Path, err))
}

func (w *FleetWatcher) fileReadable(ctx context.Context) {
	w.mu.Lock()
	w.fileErrShown = ""
	w.mu.Unlock()
	w.Alerts.Resolve(ctx, alertkey.FleetFile, "The fleet file can be read again", w.Path)
}
