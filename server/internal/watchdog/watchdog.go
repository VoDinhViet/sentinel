// Package watchdog raises the alerts only the control plane can raise: an agent that has gone quiet, and
// an agent whose certificate is about to end. It does not repeat what an agent reports of itself (a failed
// backup reaches that agent's own notifiers); it says what an agent cannot say.
package watchdog

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"sentinel/pkg/alert"
	"sentinel/pkg/notify"
	"sentinel/server/internal/enroll"
	"sentinel/server/internal/inventory"
)

// Alert keys. They are stored: do not change their form without a migration.
const (
	offlinePrefix = "agent:"
	certPrefix    = "agent-cert:"
)

// OfflineKey is the alert of an agent that is silent.
func OfflineKey(name string) string { return offlinePrefix + name }

// CertKey is the alert of an agent whose certificate is about to end.
func CertKey(name string) string { return certPrefix + name }

// CertWarnAt is how close to its end a certificate is when its agent is warned about. An agent renews at 30
// days, so an alert at 14 means the renewal has been failing for two weeks.
const CertWarnAt = 14 * 24 * time.Hour

// FirstReportGrace is how long an agent that has joined but never reported is left alone before it is called
// offline: between `sentinel agent join` and the edit of the config and the restart there is a while in
// which it is not at fault.
const FirstReportGrace = 30 * time.Minute

// Every is how often Run looks at the agents.
const Every = 30 * time.Second

// Watchdog looks at the agents and opens or closes their alerts.
type Watchdog struct {
	Alerts       *alert.Manager
	Agents       *enroll.Service
	Inventory    *inventory.Service
	OfflineAfter time.Duration
	// Now is the time; nil means the clock. Started is when the control plane started: for OfflineAfter after
	// that no agent is called offline, because they have not had time to find it again.
	Now     func() time.Time
	Started time.Time
}

func (w *Watchdog) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Run checks every Every until ctx is done, and sends again the messages no notifier took.
func (w *Watchdog) Run(ctx context.Context) error {
	ticker := time.NewTicker(Every)
	defer ticker.Stop()
	for {
		w.Check(ctx)
		w.Alerts.RetryPending(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Check is one round over the agents.
func (w *Watchdog) Check(ctx context.Context) {
	agents, err := w.Agents.List(ctx)
	if err != nil {
		slog.Error("watchdog: list agents", "err", err)
		return
	}
	now := w.now()
	graceOver := now.Sub(w.Started) >= w.OfflineAfter
	var offlineKeys, certKeys []string
	for _, agent := range agents {
		if agent.State != "active" {
			continue
		}
		offlineKeys, certKeys = append(offlineKeys, OfflineKey(agent.Name)), append(certKeys, CertKey(agent.Name))
		w.checkOffline(ctx, agent, now, graceOver)
		w.checkCert(ctx, agent, now)
	}
	w.Alerts.ResolveMissing(ctx, offlinePrefix, offlineKeys, "Server no longer watched")
	w.Alerts.ResolveMissing(ctx, certPrefix, certKeys, "Server no longer watched")
}

func (w *Watchdog) checkOffline(ctx context.Context, agent enroll.Agent, now time.Time, graceOver bool) {
	key := OfflineKey(agent.Name)
	status := inventory.StatusOf(agent.State, agent.EnrolledAt, agent.LastSeen, now, w.OfflineAfter)
	switch {
	case status == inventory.Offline && graceOver && !w.stillJoining(ctx, agent, now):
		w.Alerts.Fire(ctx, key, notify.Critical, agent.Name+" is offline", offlineBody(agent, now, w.OfflineAfter))
	case status == inventory.Online && w.Alerts.Firing(key):
		w.Alerts.Resolve(ctx, key, agent.Name+" is back", "It reported again.")
	}
}

// stillJoining says the agent has not sent its first report and joined less than FirstReportGrace ago.
func (w *Watchdog) stillJoining(ctx context.Context, agent enroll.Agent, now time.Time) bool {
	if now.Sub(agent.EnrolledAt) >= FirstReportGrace {
		return false
	}
	reported, err := w.Inventory.HasReported(ctx, agent.Name)
	if err != nil {
		slog.Error("watchdog: read reports", "agent", agent.Name, "err", err)
		return false
	}
	return !reported
}

func offlineBody(agent enroll.Agent, now time.Time, after time.Duration) string {
	if agent.LastSeen.IsZero() {
		return fmt.Sprintf("It joined at %s and has not reported since.", agent.EnrolledAt.Format("2006-01-02 15:04"))
	}
	return fmt.Sprintf("Last heard from at %s (%s ago); it is offline after %s of silence.",
		agent.LastSeen.Format("2006-01-02 15:04"), now.Sub(agent.LastSeen).Round(time.Second), after)
}

func (w *Watchdog) checkCert(ctx context.Context, agent enroll.Agent, now time.Time) {
	key := CertKey(agent.Name)
	if agent.CertNotAfter.IsZero() {
		return
	}
	left := agent.CertNotAfter.Sub(now)
	switch {
	case left < CertWarnAt:
		w.Alerts.Fire(ctx, key, notify.Warning, agent.Name+": the certificate is about to end",
			fmt.Sprintf("It ends at %s (%s left). An agent renews its certificate by itself at 30 days: look at why this one did not.",
				agent.CertNotAfter.Format("2006-01-02 15:04"), left.Round(time.Hour)))
	case w.Alerts.Firing(key):
		w.Alerts.Resolve(ctx, key, agent.Name+": the certificate was renewed", "")
	}
}
