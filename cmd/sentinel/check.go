package main

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/uptime"
)

func (a *app) checkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Chạy mọi uptime check một lần và in kết quả",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			if len(cfg.UptimeChecks) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no uptime checks configured")
				return nil
			}
			results := uptime.ProbeAll(cmd.Context(), cfg.UptimeChecks)

			table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(table, "NAME\tENDPOINT\tSTATUS\tLATENCY\tDETAIL")
			failed := 0
			for i, check := range cfg.UptimeChecks {
				result := results[i]
				status, detail := "UP", ""
				if !result.OK {
					status, detail, failed = "DOWN", result.Error, failed+1
				}
				if !result.CertExpiry.IsZero() {
					detail = strings.TrimSpace(detail + " cert expires " + result.CertExpiry.Format("2006-01-02"))
				}
				fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", check.Name, uptime.Endpoint(check), status, result.Latency.Round(time.Millisecond), detail)
			}
			table.Flush()
			if failed > 0 {
				return fmt.Errorf("%d of %d checks failed", failed, len(cfg.UptimeChecks))
			}
			return nil
		},
	}
}
