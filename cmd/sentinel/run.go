package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/alert"
	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/daemon"
	"sentinel/internal/store"
	"sentinel/internal/uptime"
)

func newAlertManager(cfg *config.Config, st *store.Store) *alert.Manager {
	return &alert.Manager{
		Notifier: newNotifier(cfg),
		Store:    st,
		Server:   cfg.ServerName,
		Cooldown: cfg.Alert.Cooldown,
	}
}

func (a *app) runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the daemon: scheduled backups and uptime checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if a.configErr != nil {
				return a.configErr
			}
			localRaw, err := os.ReadFile(a.configPath)
			if err != nil {
				return err
			}
			cfg, err := config.Parse(localRaw)
			if err != nil {
				return err
			}
			st, err := a.openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			build := func(cfg *config.Config) (*daemon.Daemon, error) {
				alerts := newAlertManager(cfg, st)
				if err := alerts.Load(ctx); err != nil {
					return nil, fmt.Errorf("load alert state: %w", err)
				}
				return &daemon.Daemon{
					Config: cfg,
					Store:  st,
					Alerts: alerts,
					Runner: &backup.Runner{
						Config: cfg, Store: st,
						LockRetries: 3, LockRetryDelay: 5 * time.Minute,
					},
					Tasks: periodicTasks(cfg, st, alerts),
				}, nil
			}

			if failed := printFindings(os.Stderr, checkBinaries(ctx, cfg)); failed > 0 {
				return errors.New("required tools are missing; run `sentinel doctor` for details")
			}
			d, err := build(cfg)
			if err != nil {
				return err
			}
			return d.Run(ctx)
		},
	}
}

// periodicTasks returns the uptime tasks for the daemon.
func periodicTasks(cfg *config.Config, st *store.Store, alerts *alert.Manager) []daemon.Task {
	var tasks []daemon.Task
	tracker := &daemon.UptimeTracker{Alerts: alerts, Store: st}
	for _, c := range cfg.UptimeChecks {
		tasks = append(tasks, daemon.Task{
			Name:  "uptime:" + c.Name,
			Every: c.Interval,
			Run: func(ctx context.Context) {
				tracker.Observe(ctx, c, uptime.Probe(ctx, c))
			},
		})
	}
	return tasks
}
