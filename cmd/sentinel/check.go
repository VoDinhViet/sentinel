package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/config"
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
			return a.runCheck(cmd, output)
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}

// runCheck probes every uptime check once, prints the outcomes and fails when any check is down.
func (a *app) runCheck(cmd *cobra.Command, output string) error {
	if err := checkOutput(output); err != nil {
		return err
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	if len(cfg.UptimeChecks) == 0 {
		if output == outputJSON {
			return writeJSON(w, checkReport{Checks: []probeOutcome{}})
		}
		fmt.Fprintln(w, "no uptime checks configured")
		return nil
	}

	outcomes, failed := probeOutcomes(cmd.Context(), cfg.UptimeChecks)
	if output == outputJSON {
		err = writeJSON(w, checkReport{Checks: outcomes})
	} else {
		err = printOutcomes(w, outcomes)
	}
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d checks failed", failed, len(cfg.UptimeChecks))
	}
	return nil
}

// probeOutcomes probes the checks at once and returns their outcomes in order, with how many failed.
func probeOutcomes(ctx context.Context, checks []config.UptimeCheck) (outcomes []probeOutcome, failed int) {
	probes := uptime.ProbeAll(ctx, checks)
	outcomes = make([]probeOutcome, len(checks))
	for i, check := range checks {
		outcomes[i] = newProbeOutcome(check, probes[i])
		if !probes[i].OK {
			failed++
		}
	}
	return outcomes, failed
}

func newProbeOutcome(check config.UptimeCheck, probe uptime.Result) probeOutcome {
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
	return outcome
}

func printOutcomes(w io.Writer, outcomes []probeOutcome) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tENDPOINT\tSTATUS\tLATENCY\tDETAIL")
	for _, outcome := range outcomes {
		status, detail := outcomeStatus(outcome)
		latency := time.Duration(outcome.LatencyMs) * time.Millisecond
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", outcome.Name, outcome.Endpoint, status, latency, detail)
	}
	return table.Flush()
}

// outcomeStatus is the STATUS and DETAIL columns of an outcome: the error when it is down, and the
// certificate expiry when there is one.
func outcomeStatus(outcome probeOutcome) (status, detail string) {
	status = "UP"
	if !outcome.OK {
		status, detail = "DOWN", outcome.Error
	}
	if outcome.CertExpiry != nil {
		detail = strings.TrimSpace(detail + " cert expires " + outcome.CertExpiry.Format(time.DateOnly))
	}
	return status, detail
}
