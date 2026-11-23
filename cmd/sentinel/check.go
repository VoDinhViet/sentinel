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
		Short: "Run every uptime check once and print the results",
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

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTARGET\tSTATUS\tLATENCY\tDETAIL")
			failed := 0
			for i, c := range cfg.UptimeChecks {
				r := results[i]
				status, detail := "UP", ""
				if !r.OK {
					status, detail, failed = "DOWN", r.Error, failed+1
				}
				if !r.CertExpiry.IsZero() {
					detail = strings.TrimSpace(detail + " cert expires " + r.CertExpiry.Format("2006-01-02"))
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Name, uptime.Endpoint(c), status, r.Latency.Round(time.Millisecond), detail)
			}
			w.Flush()
			if failed > 0 {
				return fmt.Errorf("%d of %d checks failed", failed, len(cfg.UptimeChecks))
			}
			return nil
		},
	}
}
