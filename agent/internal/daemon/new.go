package daemon

import (
	"context"
	"fmt"
	"slices"
	"time"

	"sentinel/agent/internal/alert"
	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/notify"
	"sentinel/agent/internal/store"
)

// New builds the daemon for cfg: the alert manager (restored from the store, so a restart
// does not announce open alerts again), the backup runner, one task per uptime check, and the tasks
// that watch the disks, the other servers of the fleet and the heartbeat.
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
		Config:       cfg,
		Store:        st,
		ShutdownWait: cfg.ShutdownWait,
		Alerts:       alerts,
		Runner: &backup.Runner{
			Config: cfg, Store: st,
			LockRetries: 3, LockRetryDelay: 5 * time.Minute,
			DeferPrune: true, // the daemon prunes each repository once, on prune_schedule
		},
		Tasks: slices.Concat(uptimeTasks(cfg, st, alerts), diskTask(cfg, alerts), fleetTask(cfg, alerts), heartbeatTask(cfg)),
	}, nil
}
