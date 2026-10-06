package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"sentinel/internal/alert"
	"sentinel/internal/notify"
)

// Watchdog alerts when agents stop reporting and expires stuck commands.
type Watchdog struct {
	Server *Server
	Alerts *alert.Manager
	Now    func() time.Time
}

func (w *Watchdog) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Check runs one pass.
func (w *Watchdog) Check(ctx context.Context) {
	svc, cfg := w.Server.Service, w.Server.Config
	agents, err := svc.ListAgents(ctx)
	if err != nil {
		slog.Error("watchdog: list agents", "error", err)
		return
	}
	now := w.now()
	for _, a := range agents {
		if a.LastSeen.IsZero() {
			continue // never connected: nothing has gone wrong yet
		}
		key := "agent-offline:" + a.Name
		if a.Online(now, cfg.OfflineAfter) {
			w.Alerts.Resolve(ctx, key, a.Name+" is reporting again", "")
		} else {
			w.Alerts.Fire(ctx, key, notify.Critical, a.Name+" is offline",
				fmt.Sprintf("No report since %s (%s ago). The agent may be down or unable to reach the server.",
					a.LastSeen.Format("2006-01-02 15:04"), now.Sub(a.LastSeen).Round(time.Second)))
		}
	}
	// Backups have a 6h default timeout; allow an hour more for the result.
	if err := svc.ExpireCommands(ctx, 7*time.Hour, 24*time.Hour); err != nil {
		slog.Error("watchdog: expire commands", "error", err)
	}
}

// Run checks every interval and prunes old history daily until ctx ends.
func (w *Watchdog) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		w.Check(ctx)
		if time.Since(lastPrune) > 24*time.Hour {
			if err := w.Server.Service.Prune(ctx, w.now().Add(-30*24*time.Hour)); err != nil {
				slog.Error("watchdog: prune", "error", err)
			}
			lastPrune = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
