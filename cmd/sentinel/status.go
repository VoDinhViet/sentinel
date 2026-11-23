package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/status"
)

func (a *app) statusCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Xem các backup gần nhất, cảnh báo đang mở và uptime check",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(output); err != nil {
				return err
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
			report, err := status.Build(cmd.Context(), cfg, st, time.Now())
			if err != nil {
				return err
			}
			if output == outputJSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			return printStatus(cmd.OutOrStdout(), report)
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

// ago formats a time as "2006-01-02 15:04 (3m0s ago)".
func ago(t, now time.Time) string {
	return t.Local().Format("2006-01-02 15:04") + " (" + now.Sub(t).Round(time.Minute).String() + " ago)"
}

// relativeAgo is a short "5 phút trước" for the interactive screen.
func relativeAgo(t, now time.Time) string {
	elapsed := now.Sub(t)
	switch {
	case t.IsZero():
		return "chưa có"
	case elapsed < time.Minute:
		return "vừa xong"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d phút trước", int(elapsed/time.Minute))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%d giờ trước", int(elapsed/time.Hour))
	}
	return fmt.Sprintf("%d ngày trước", int(elapsed/(24*time.Hour)))
}

func printStatus(w io.Writer, report status.Report) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	now := report.GeneratedAt

	fmt.Fprintf(w, "Server: %s\n\n", report.Server)

	fmt.Fprintf(w, "Open alerts: %d\n", len(report.Alerts))
	for _, alert := range report.Alerts {
		fmt.Fprintf(table, "  %s\t%s\tsince %s\n", alert.Key, alert.Message, ago(alert.Since, now))
	}
	table.Flush()

	if len(report.Running) > 0 {
		fmt.Fprintln(w, "\nRunning now:")
		for _, running := range report.Running {
			note := ""
			switch {
			case running.Stalled:
				note = "  (no new data for 10+ minutes)"
			case running.WillExceedTimeout:
				note = "  (at this speed it will pass the job's timeout)"
			}
			fmt.Fprintf(table, "  %s\t%s\t%s%s\n", running.Job, running.Repository, running.Summary, note)
		}
		table.Flush()
	}

	fmt.Fprintln(w, "\nLast backups:")
	if len(report.Jobs) == 0 {
		fmt.Fprintln(w, "  none yet")
	}
	for _, backup := range report.Jobs {
		summary := backup.Result
		if backup.Success {
			summary += fmt.Sprintf("  %.8s", backup.SnapshotID)
		}
		fmt.Fprintf(table, "  %s\t%s\t%s\t%s\n", backup.Job, backup.Repository, ago(backup.FinishedAt, now), summary)
	}
	table.Flush()

	fmt.Fprintln(w, "\nUptime checks:")
	if len(report.Checks) == 0 {
		fmt.Fprintln(w, "  none yet")
	}
	for _, check := range report.Checks {
		state := "UP"
		if !check.OK {
			state = "DOWN: " + check.Error
		}
		latency := (time.Duration(check.LatencyMs) * time.Millisecond).Round(time.Millisecond)
		fmt.Fprintf(table, "  %s\t%s\t%s\t%s\n", check.Name, state, latency, ago(check.At, now))
	}
	return table.Flush()
}
