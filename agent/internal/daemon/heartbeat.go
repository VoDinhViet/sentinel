package daemon

import (
	"context"
	"log/slog"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/notify"
)

// heartbeatTask requests the heartbeat URL at start and then every cfg.Heartbeat.Every, or is
// absent when no URL is configured. A failed request is logged: the service on the other end is
// the one that raises the alarm, so there is nothing more for the daemon to do about it.
func heartbeatTask(cfg *config.Config) []Task {
	if cfg.Heartbeat.URL == "" {
		return nil
	}
	endpoint := cfg.Heartbeat.URL
	return []Task{{
		Name:    "heartbeat",
		Every:   cfg.Heartbeat.Every,
		AtStart: true,
		Run: func(ctx context.Context) {
			if err := notify.Heartbeat(ctx, endpoint); err != nil {
				slog.Warn("heartbeat failed", "error", err)
			}
		},
	}}
}
