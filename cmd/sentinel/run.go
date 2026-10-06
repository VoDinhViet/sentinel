package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/agent"
	"sentinel/internal/alert"
	"sentinel/internal/api"
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

// How often an agent talks to the server.
const (
	reportEvery     = 30 * time.Second
	configPollEvery = 30 * time.Second
)

func (a *app) runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the agent: scheduled backups, monitoring and uptime checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
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

			build := func(cfg *config.Config) (*scheduler.Daemon, error) {
				alerts := newAlertManager(cfg, st)
				if err := alerts.Load(ctx); err != nil {
					return nil, fmt.Errorf("load alert state: %w", err)
				}
				return &scheduler.Daemon{
					Config: cfg,
					Store:  st,
					Alerts: alerts,
					Backups: &backup.Runner{
						Config: cfg, Store: st,
						LockRetries: 3, LockRetryDelay: 5 * time.Minute,
					},
					Tasks: periodicTasks(cfg, st, alerts),
				}, nil
			}

			if cfg.Central == nil {
				if failed := printFindings(os.Stderr, checkBinaries(ctx, cfg)); failed > 0 {
					return errors.New("required tools are missing; run `sentinel doctor` for details")
				}
				d, err := build(cfg)
				if err != nil {
					return err
				}
				return d.Run(ctx)
			}
			return runConnected(ctx, cfg, localRaw, st, build)
		},
	}
}

// runConnected runs the daemon under a supervisor and talks to the server.
func runConnected(ctx context.Context, local *config.Config, localRaw []byte, st *store.Store,
	build func(*config.Config) (*scheduler.Daemon, error)) error {
	client := agent.NewClient(local.Central.URL, local.Central.Token)
	sup := agent.NewSupervisor(build)

	cfg, cfgVersion := local, int64(0)
	if local.Central.Managed {
		cs := &agent.ConfigSync{
			Client:     client,
			LocalRaw:   localRaw,
			CachePath:  filepath.Join(filepath.Dir(local.Store), "managed-config.json"),
			Supervisor: sup,
		}
		var err error
		if cfg, cfgVersion, err = cs.Initial(ctx); err != nil {
			return err
		}
		go cs.Run(ctx, configPollEvery)
	}
	// A managed config can be fixed remotely, so missing tools only warn.
	if failed := printFindings(os.Stderr, checkBinaries(ctx, cfg)); failed > 0 && !local.Central.Managed {
		return errors.New("required tools are missing; run `sentinel doctor` for details")
	}

	hostname, _ := os.Hostname()
	rep := &agent.Reporter{
		Client: client,
		Store:  st,
		Info: func() api.Report {
			r := api.Report{Hostname: hostname, Version: version, Jobs: []api.Job{}, Checks: []string{}}
			d, v := sup.Current()
			if d == nil {
				return r
			}
			r.ConfigVersion = v
			for _, b := range d.Config.Backups {
				r.Jobs = append(r.Jobs, api.Job{Name: b.Name, Type: b.Type, Schedule: b.Schedule, Repositories: b.Repositories})
			}
			for _, c := range d.Config.Uptime {
				r.Checks = append(r.Checks, c.Name)
			}
			return r
		},
	}
	go func() {
		// Wait for the first daemon so the first report lists the jobs.
		for d, _ := sup.Current(); d == nil && ctx.Err() == nil; d, _ = sup.Current() {
			time.Sleep(100 * time.Millisecond)
		}
		rep.Run(ctx, reportEvery)
	}()
	go (&agent.CommandRunner{Client: client, Current: sup.Current}).Run(ctx)

	return sup.Run(ctx, cfg, cfgVersion)
}

// periodicTasks returns the uptime and monitoring tasks for the daemon.
func periodicTasks(cfg *config.Config, st *store.Store, alerts *alert.Manager) []scheduler.Task {
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
