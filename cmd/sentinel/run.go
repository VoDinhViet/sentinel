package main

import (
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"sentinel/internal/daemon"
	"sentinel/internal/doctor"
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
			return dm.Run(ctx)
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
