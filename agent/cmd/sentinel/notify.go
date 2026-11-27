package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"sentinel/agent/internal/notify"
)

func (a *cliApp) notifyCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notify", Short: "Kênh gửi thông báo"}
	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Gửi tin nhắn thử tới mọi kênh thông báo đã cấu hình",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			multi := notify.NewMulti(cfg.Notifiers)
			if len(multi.Notifiers) == 0 {
				return errors.New("no notifiers configured")
			}
			multi.Attempts = 1
			errs := multi.SendEach(cmd.Context(), notify.Message{
				Level:  notify.Info,
				Server: cfg.ServerName,
				Title:  "Test notification",
				Body:   "Sent by `sentinel notify test` at " + time.Now().Format(time.RFC3339),
			})
			for i, notifier := range multi.Notifiers {
				status := "ok"
				if errs[i] != nil {
					status = "FAILED: " + errs[i].Error()
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-10s %s\n", notifier.Name(), status)
			}
			if err := errors.Join(errs...); err != nil {
				return errors.New("some notifiers failed")
			}
			return nil
		},
	})
	return cmd
}
