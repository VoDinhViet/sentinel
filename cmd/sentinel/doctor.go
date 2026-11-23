package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"sentinel/internal/doctor"
)

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

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Kiểm tra config, công cụ, kho backup và nơi lưu trữ",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			stdout := cmd.OutOrStdout()
			cfg, err := a.loadConfig()
			if err != nil {
				fmt.Fprintf(stdout, "  FAIL  %-28s %v\n", strings.TrimSpace("config "+a.configPath), err)
				return errors.New("doctor found problems")
			}
			if printFindings(stdout, doctor.Run(cmd.Context(), cfg, a.configPath, a.openStore)) > 0 {
				return errors.New("doctor found problems")
			}
			return nil
		},
	}
}
