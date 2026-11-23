package daemon

import (
	"context"
	"fmt"
	"time"

	"sentinel/internal/alert"
	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/notify"
	"sentinel/internal/store"
)

// New builds the daemon for cfg: the alert manager (restored from the store, so a restart
// does not announce open alerts again), the backup runner and one task per uptime check.
func New(ctx context.Context, cfg *config.Config, st *store.Store) (*Daemon, error) {
	alerts := &alert.Manager{
		Notifier: notify.NewMulti(cfg.Notifiers),
		Store:    st,
		Server:   cfg.ServerName,
		Cooldown: cfg.Alert.Cooldown,
	}
	if err := alerts.Load(ctx); err != nil {
		return nil, fmt.Errorf("load alert state: %w", err)
	}
	return &Daemon{
		Config: cfg,
		Store:  st,
		Alerts: alerts,
		Runner: &backup.Runner{
			Config: cfg, Store: st,
			LockRetries: 3, LockRetryDelay: 5 * time.Minute,
		},
		Tasks: uptimeTasks(cfg, st, alerts),
	}, nil
}
