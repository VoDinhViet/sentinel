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
	"sentinel/internal/monitor"
	"sentinel/internal/scheduler"
	"sentinel/internal/store"
	"sentinel/internal/uptime"
)

func newAlertManager(cfg *config.Config, st *store.Store) *alert.Manager {
	return &alert.Manager{
		Sender:   newNotifier(cfg),
		Store:    st,
		Server:   cfg.ServerName,
		Cooldown: cfg.Alert.Cooldown,
	}
}

func (a *app) runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the agent: scheduled backups, monitoring and uptime checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			if failed := printFindings(os.Stderr, checkBinaries(ctx, cfg)); failed > 0 {
				return errors.New("required tools are missing; run `sentinel doctor` for details")
			}
			st, err := a.openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			alerts := newAlertManager(cfg, st)
			if err := alerts.Load(ctx); err != nil {
				return fmt.Errorf("load alert state: %w", err)
			}
			d := &scheduler.Daemon{
				Config: cfg,
				Store:  st,
				Alerts: alerts,
				Backups: &backup.Runner{
					Config: cfg, Store: st,
					LockRetries: 3, LockRetryDelay: 5 * time.Minute,
				},
				Tasks: a.periodicTasks(cfg, st, alerts),
			}
			return d.Run(ctx)
		},
	}
}

// periodicTasks returns the uptime and monitoring tasks for the daemon.
func (a *app) periodicTasks(cfg *config.Config, st *store.Store, alerts *alert.Manager) []scheduler.Task {
	var tasks []scheduler.Task
	tracker := &uptime.Tracker{Alerts: alerts, Store: st}
	for _, c := range cfg.Uptime {
		tasks = append(tasks, scheduler.Task{
			Name:  "uptime:" + c.Name,
			Every: c.Interval,
			Run: func(ctx context.Context) {
				tracker.Observe(ctx, c, uptime.Check(ctx, c))
			},
		})
	}
	if cfg.Monitor != nil {
		mon := &monitor.Monitor{Thresholds: cfg.Monitor.Thresholds, Source: monitor.System{}, Alerts: alerts, Store: st}
		tasks = append(tasks, scheduler.Task{Name: "monitor", Every: cfg.Monitor.Interval, Run: mon.Tick})
	}
	return tasks
}
