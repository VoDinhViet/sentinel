package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"sentinel/internal/api"
	"sentinel/internal/app"
	"sentinel/internal/config"
	"sentinel/internal/daemon"
	"sentinel/internal/doctor"
	"sentinel/internal/store"
)

func (a *cliApp) runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Chạy daemon: backup theo lịch và uptime check",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg, err := a.readConfig()
			if err != nil {
				return err
			}
			st, err := a.openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()

			if failed := printFindings(os.Stderr, doctor.Tools(ctx, cfg)); failed > 0 {
				return errors.New("required tools are missing; run `sentinel doctor` for details")
			}
			dm, err := daemon.New(ctx, cfg, st)
			if err != nil {
				return err
			}
			dm.ForceStop = forceStopSignal()
			stopAPI, err := a.startAPI(ctx, cfg, st)
			if err != nil {
				return err
			}
			defer stopAPI() // also when the daemon fails to start while ctx is still live
			return dm.Run(ctx)
		},
	}
}

// startAPI listens on api.listen, when it is set, and serves the API until ctx is done or the
// returned function is called, which waits for the server to stop. A port that cannot be taken is an error here, at
// startup, not a line in the log of a daemon that goes on without its API.
func (a *cliApp) startAPI(ctx context.Context, cfg *config.Config, st *store.Store) (stop func(), err error) {
	if cfg.API.Listen == "" {
		return func() {}, nil
	}
	ln, err := net.Listen("tcp", cfg.API.Listen)
	if err != nil {
		return nil, fmt.Errorf("api.listen: %w", err)
	}
	handler := api.New(app.New(cfg, st, a.configPath), api.Options{Token: cfg.API.Token, Version: version}).Handler()
	apiCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := api.Serve(apiCtx, ln, handler); err != nil {
			slog.Error("api stopped", "err", err)
		}
	}()
	slog.Info("api listening", "addr", ln.Addr().String())
	return func() { cancel(); <-done }, nil
}

// forceStopSignal is closed by the second stop signal (the first one already ended the daemon's
// context): the daemon then stops waiting for running backups and cancels them.
func forceStopSignal() <-chan struct{} {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	force := make(chan struct{})
	go func() {
		<-signals // the signal that asked the daemon to stop
		<-signals
		close(force)
	}()
	return force
}
