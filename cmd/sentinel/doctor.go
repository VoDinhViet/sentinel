package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"sentinel/internal/doctor"
)

// doctorReport is the JSON of `sentinel doctor -o json`.
type doctorReport struct {
	OK       bool             `json:"ok"`
	Findings []findingOutcome `json:"findings"`
}

// findingOutcome is a doctor.Finding in JSON; Status is "ok", "warn" or "fail".
type findingOutcome struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Info   string `json:"info,omitempty"`
	Error  string `json:"error,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

func printFindings(w io.Writer, findings []doctor.Finding) (failed int) {
	for _, finding := range findings {
		if finding.Err != nil && finding.Warn {
			fmt.Fprintf(w, "  WARN  %-28s %v\n", finding.Name, finding.Err)
		} else if finding.Err != nil {
			failed++
			fmt.Fprintf(w, "  FAIL  %-28s %v\n", finding.Name, finding.Err)
		} else {
			fmt.Fprintf(w, "  ok    %-28s %s\n", finding.Name, finding.Info)
		}
	}
	return failed
}

// findingsReport turns the findings into the JSON report: ok is false when any finding failed
// (a warning does not fail doctor).
func findingsReport(findings []doctor.Finding) doctorReport {
	report := doctorReport{OK: true, Findings: make([]findingOutcome, 0, len(findings))}
	for _, finding := range findings {
		outcome := findingOutcome{Name: finding.Name, Status: "ok", Info: finding.Info, Hint: finding.Hint}
		switch {
		case finding.Err != nil && finding.Warn:
			outcome.Status, outcome.Error = "warn", finding.Err.Error()
		case finding.Err != nil:
			outcome.Status, outcome.Error = "fail", finding.Err.Error()
			report.OK = false
		}
		report.Findings = append(report.Findings, outcome)
	}
	return report
}

func (a *cliApp) doctorCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Kiểm tra config, công cụ, kho backup và nơi lưu trữ",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput(output); err != nil {
				return err
			}
			var findings []doctor.Finding
			cfg, err := a.loadConfig()
			if err != nil {
				findings = []doctor.Finding{{Name: strings.TrimSpace("config " + a.configPath), Err: err}}
			} else {
				findings = doctor.Run(cmd.Context(), cfg, a.configPath, a.openStore)
			}

			stdout := cmd.OutOrStdout()
			var failed int
			if output == outputJSON {
				report := findingsReport(findings)
				if err := writeJSON(stdout, report); err != nil {
					return err
				}
				if !report.OK {
					failed = 1
				}
			} else {
				failed = printFindings(stdout, findings)
			}
			if failed > 0 {
				return errors.New("doctor found problems")
			}
			return nil
		},
	}
	addOutputFlag(cmd, &output)
	return cmd
}
