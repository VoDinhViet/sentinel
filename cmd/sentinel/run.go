package main

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"sentinel/internal/daemon"
	"sentinel/internal/doctor"
)

func (a *app) runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the daemon: scheduled backups and uptime checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg, err := a.loadConfig()
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
			d, err := daemon.New(ctx, cfg, st)
			if err != nil {
				return err
			}
			return d.Run(ctx)
		},
	}
}
