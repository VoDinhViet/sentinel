package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/tui"
)

func (a *cliApp) manageCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "manage",
		Aliases: []string{"tui"},
		Short:   "Giao diện terminal: backup, snapshot, khôi phục, uptime check và cảnh báo",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return a.runManage(cmd) },
	}
}

// runManage opens the terminal manager on the loaded config.
func (a *cliApp) runManage(cmd *cobra.Command) error {
	if !stdoutIsTerminal() {
		return fmt.Errorf("manage cần chạy trong terminal; trong script hãy dùng `sentinel status --output json`")
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
	return tui.RunManage(cmd.Context(), app.New(cfg, st, a.configPath))
}

func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
