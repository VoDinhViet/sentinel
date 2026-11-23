package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) statusCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show last backups, open alerts, and checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output != "text" && output != "json" {
				return fmt.Errorf("--output: want text or json, got %q", output)
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			st, err := a.openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			report, err := buildStatusReport(cmd.Context(), cfg, st, time.Now())
			if err != nil {
				return err
			}
			if output == "json" {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			}
			return printStatus(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "text or json")
	return cmd
}

// ago formats a time as "2006-01-02 15:04 (3m0s ago)".
func ago(t, now time.Time) string {
	return t.Local().Format("2006-01-02 15:04") + " (" + now.Sub(t).Round(time.Minute).String() + " ago)"
}

func printStatus(out io.Writer, report statusReport) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	now := report.GeneratedAt

	fmt.Fprintf(out, "Server: %s\n\n", report.Server)

	fmt.Fprintf(out, "Open alerts: %d\n", len(report.Alerts))
	for _, alert := range report.Alerts {
		fmt.Fprintf(w, "  %s\t%s\tsince %s\n", alert.Key, alert.Message, ago(alert.Since, now))
	}
	w.Flush()

	fmt.Fprintln(out, "\nLast backups:")
	if len(report.Backups) == 0 {
		fmt.Fprintln(out, "  none yet")
	}
	for _, backup := range report.Backups {
		result := backup.Result
		if backup.Success {
			result += fmt.Sprintf("  %.8s", backup.SnapshotID)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", backup.Job, backup.Repository, ago(backup.FinishedAt, now), result)
	}
	w.Flush()

	fmt.Fprintln(out, "\nUptime checks:")
	if len(report.Checks) == 0 {
		fmt.Fprintln(out, "  none yet")
	}
	for _, check := range report.Checks {
		state := "UP"
		if !check.OK {
			state = "DOWN: " + check.Error
		}
		latency := (time.Duration(check.LatencyMs) * time.Millisecond).Round(time.Millisecond)
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", check.Name, state, latency, ago(check.At, now))
	}
	return w.Flush()
}
