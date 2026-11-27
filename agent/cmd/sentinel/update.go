package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"sentinel/agent/internal/selfupdate"
)

// updateInstallCmd and updateRollbackCmd are what the daemon runs in a transient unit (outside its sandbox, as root)
// to put a staged binary in place or the previous one back. They take no argument: what they act on is the note in the
// state directory, and the binary they replace is the one they are.
func (a *cliApp) updateInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "update-install",
		Short:  "Đặt binary đã tải vào chỗ binary đang chạy (daemon gọi, không chạy tay)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, exe, err := a.updatePaths()
			if err != nil {
				return err
			}
			if err := selfupdate.Install(dir, exe); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "the new binary is in place; the previous one is kept as", exe+".prev")
			return nil
		},
	}
}

func (a *cliApp) updateRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "update-rollback",
		Short:  "Đặt lại binary trước khi cập nhật (daemon gọi, không chạy tay)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, exe, err := a.updatePaths()
			if err != nil {
				return err
			}
			if err := selfupdate.Rollback(dir, exe, selfupdate.WhyGoBack(dir)); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "the previous binary is back")
			return nil
		},
	}
}

// updatePaths are the directory of the update and the path of this binary.
func (a *cliApp) updatePaths() (dir, exe string, err error) {
	cfg, err := a.readConfig()
	if err != nil {
		return "", "", err
	}
	exe, err = os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("find this binary: %w", err)
	}
	return selfupdate.Dir(cfg.StorePath), exe, nil
}
