package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"sentinel/agent/internal/daemon"
	"sentinel/agent/internal/doctor"
)

// errRestart is why the daemon stops after an update or a rollback: it exits with an error so that systemd, which
// restarts the daemon on failure only, starts it again.
var errRestart = errors.New("restarting to run the other binary")

func (a *cliApp) runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Chạy daemon: backup theo lịch và uptime check",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, restart := context.WithCancelCause(cmd.Context())
			defer restart(nil)
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
			for _, block := range cfg.Deprecated() {
				slog.Warn("config block ignored: the daemon listens on no port and does not watch other servers; remove it", "block", block+":")
			}
			stopReporter := a.startReporter(ctx, cfg, st, func() { restart(errRestart) })
			defer stopReporter()
			if err := dm.Run(ctx); err != nil {
				return err
			}
			if errors.Is(context.Cause(ctx), errRestart) {
				return errRestart // a failure to systemd: Restart=on-failure starts the daemon again, now the other binary
			}
			return nil
		},
	}
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
