package main

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show last backups, open alerts, latest metrics and checks",
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
			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			ago := func(t time.Time) string {
				return t.Local().Format("2006-01-02 15:04") + " (" + time.Since(t).Round(time.Minute).String() + " ago)"
			}

			fmt.Fprintf(out, "Server: %s\n\n", cfg.ServerName)

			alerts, err := st.Alerts(ctx, true)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Open alerts: %d\n", len(alerts))
			for _, al := range alerts {
				fmt.Fprintf(w, "  %s\t%s\tsince %s\n", al.Key, al.Message, ago(al.Since))
			}
			w.Flush()

			runs, err := st.LatestBackups(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, "\nLast backups:")
			if len(runs) == 0 {
				fmt.Fprintln(out, "  none yet")
			}
			for _, r := range runs {
				result := "ok " + fmt.Sprintf("%.8s", r.SnapshotID)
				if !r.Success {
					result = "FAILED: " + r.Error
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", r.Job, r.Repository, ago(r.FinishedAt), result)
			}
			w.Flush()

			m, ok, err := st.LatestMetrics(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, "\nLatest metrics:")
			if ok {
				fmt.Fprintf(out, "  CPU %.1f%%  memory %.1f%%  swap %.1f%%  load %.2f  fullest disk %.1f%%  at %s\n",
					m.CPU, m.Memory, m.Swap, m.Load1, m.DiskMax, ago(m.At))
			} else {
				fmt.Fprintln(out, "  none yet (monitor not configured or daemon not running)")
			}

			checks, err := st.LatestChecks(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, "\nUptime checks:")
			if len(checks) == 0 {
				fmt.Fprintln(out, "  none yet")
			}
			for _, c := range checks {
				state := "UP"
				if !c.OK {
					state = "DOWN: " + c.Error
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", c.Name, state, c.Latency.Round(time.Millisecond), ago(c.At))
			}
			return w.Flush()
		},
	}
}
