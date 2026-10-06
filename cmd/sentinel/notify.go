package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/config"
	"sentinel/internal/notify"
)

func newNotifier(cfg *config.Config) *notify.Multi {
	return &notify.Multi{Notifiers: notify.FromConfig(cfg.Notifiers)}
}

func (a *app) notifyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notify", Short: "Notification channels"}
	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send a test message to every configured channel",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			n := newNotifier(cfg)
			if len(n.Notifiers) == 0 {
				return errors.New("no notifiers configured")
			}
			n.Attempts = 1
			errs := n.SendEach(cmd.Context(), notify.Message{
				Level:  notify.Info,
				Server: cfg.ServerName,
				Title:  "Test notification",
				Body:   "Sent by `sentinel notify test` at " + time.Now().Format(time.RFC3339),
			})
			for i, ch := range n.Notifiers {
				status := "ok"
				if errs[i] != nil {
					status = "FAILED: " + errs[i].Error()
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-10s %s\n", ch.Name(), status)
			}
			if err := errors.Join(errs...); err != nil {
				return errors.New("some channels failed")
			}
			return nil
		},
	})
	return cmd
}
