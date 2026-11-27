package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

const (
	outputText = "text"
	outputJSON = "json"
)

// addOutputFlag gives a command -o/--output, so scripts and agents can ask for JSON.
func addOutputFlag(cmd *cobra.Command, output *string) {
	cmd.Flags().StringVarP(output, "output", "o", outputText, "text hoặc json")
}

func checkOutput(output string) error {
	if output != outputText && output != outputJSON {
		return fmt.Errorf("--output: want text or json, got %q", output)
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}
