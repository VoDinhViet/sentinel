package main

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/uptime"
)

// checkReport is the JSON of `sentinel check -o json`.
type checkReport struct {
	Checks []probeOutcome `json:"checks"`
}

type probeOutcome struct {
	Name       string     `json:"name"`
	Endpoint   string     `json:"endpoint"`
	OK         bool       `json:"ok"`
	LatencyMs  int64      `json:"latency_ms"`
	Error      string     `json:"error,omitempty"`
	CertExpiry *time.Time `json:"cert_expiry,omitempty"`
}

func (a *app) checkCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Chạy mọi uptime check một lần và in kết quả",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			if len(cfg.UptimeChecks) == 0 {
				if output == outputJSON {
					return writeJSON(cmd.OutOrStdout(), checkReport{Checks: []probeOutcome{}})
				}
				fmt.Fprintln(cmd.OutOrStdout(), "no uptime checks configured")
				return nil
			}
			probes := uptime.ProbeAll(cmd.Context(), cfg.UptimeChecks)

			failed := 0
			outcomes := make([]probeOutcome, 0, len(probes))
			for i, check := range cfg.UptimeChecks {
				probe := probes[i]
				if !probe.OK {
					failed++
				}
				outcome := probeOutcome{
					Name:      check.Name,
					Endpoint:  uptime.Endpoint(check),
					OK:        probe.OK,
					LatencyMs: probe.Latency.Round(time.Millisecond).Milliseconds(),
					Error:     probe.Error,
				}
				if !probe.CertExpiry.IsZero() {
					outcome.CertExpiry = &probe.CertExpiry
				}
				outcomes = append(outcomes, outcome)
			}

			if output == outputJSON {
				if err := writeJSON(cmd.OutOrStdout(), checkReport{Checks: outcomes}); err != nil {
					return err
				}
			} else {
				printProbes(cmd, outcomes)
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d checks failed", failed, len(cfg.UptimeChecks))
			}
			return nil
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

func printProbes(cmd *cobra.Command, outcomes []probeOutcome) {
	table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tENDPOINT\tSTATUS\tLATENCY\tDETAIL")
	for _, outcome := range outcomes {
		status, detail := "UP", ""
		if !outcome.OK {
			status, detail = "DOWN", outcome.Error
		}
		if outcome.CertExpiry != nil {
			detail = strings.TrimSpace(detail + " cert expires " + outcome.CertExpiry.Format("2006-01-02"))
		}
		latency := (time.Duration(outcome.LatencyMs) * time.Millisecond).Round(time.Millisecond)
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", outcome.Name, outcome.Endpoint, status, latency, detail)
	}
	table.Flush()
}
