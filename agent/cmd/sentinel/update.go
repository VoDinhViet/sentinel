package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sentinel/agent/internal/selfupdate"
	"sentinel/agent/internal/store"
	"sentinel/pkg/agentapi"
	"sentinel/pkg/release"
)

// The commands below are what the daemon (and, for update-guard, a timer) runs in a transient unit, outside its
// sandbox, as root, to put a staged binary in place or the previous one back. They take no argument: what they act on is
// the note in the state directory, and the binary they replace is the one they are (or, when they are the
// previous binary, the one without ".prev").

// guardUnit is the timer that puts the previous binary back when the new version never gets as far as doing it itself
// (a config it cannot read, a store it cannot open, a hang): it is armed by the install, before the new binary exists.
const guardUnit = "sentinel-self-update-guard"

// systemToolDirs are the places the helpers look for systemd-run and systemctl (not the PATH).
var systemToolDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// runSystemTool runs a program of the system and returns its output; a test replaces it.
var runSystemTool = func(ctx context.Context, name string, args ...string) (string, error) {
	for _, dir := range systemToolDirs {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			out, err := exec.CommandContext(ctx, path, args...).CombinedOutput() //nolint:gosec // a fixed program and fixed arguments
			return strings.TrimSpace(string(out)), err
		}
	}
	return "", fmt.Errorf("this machine has no %s", name)
}

// executablePath is the path of this binary; a test replaces it.
var executablePath = os.Executable

func (a *cliApp) updateInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "update-install",
		Short:  "Đặt binary đã tải vào chỗ binary đang chạy (daemon gọi, không chạy tay)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.readConfig()
			if err != nil {
				return err
			}
			key, err := release.ParsePublicKey(cfg.ControlPlane.Allow.SelfUpdate.PublicKey)
			if err != nil {
				return fmt.Errorf("control_plane.allow.self_update.public_key: %w", err)
			}
			dir, exe, err := a.updatePaths()
			if err != nil {
				return err
			}
			if err := snapshotState(cmd.Context(), cfg.StorePath, dir); err != nil {
				return fmt.Errorf("keep a copy of the state database for a rollback: %w", err)
			}
			// First the guard, then the install: an install that cannot be watched is not done.
			if err := armGuard(cmd.Context(), exe, a.configPath); err != nil {
				return fmt.Errorf("arm the guard that goes back if the new version is silent: %w", err)
			}
			if err := selfupdate.Install(dir, exe, key); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "the new binary is in place; the previous one is kept as", exe+".prev")
			return nil
		},
	}
}

// armGuard starts the timer that runs the previous binary's update-guard after the trial window and a little more.
func armGuard(ctx context.Context, exe, configPath string) error {
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	after := selfupdate.TrialWindow + 2*time.Minute
	_, err = runSystemTool(ctx, "systemd-run", "--on-active="+fmt.Sprint(int(after.Seconds())), "--unit="+guardUnit, "--collect", "--quiet",
		"--", exe+".prev", "--config", absolute, "update-guard")
	return err
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
			if err := a.restoreState(dir); err != nil {
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

// updateGuardCmd runs from the timer that the install armed. If the new version confirmed itself there is nothing to
// do; if not, the previous binary is put back and the daemon is restarted, whatever state it is in.
func (a *cliApp) updateGuardCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "update-guard",
		Short:  "Quay về binary cũ nếu bản mới chưa báo cáo (timer gọi, không chạy tay)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, exe, err := a.updatePaths()
			if err != nil {
				return err
			}
			if !selfupdate.OnTrialNow(dir) {
				fmt.Fprintln(cmd.OutOrStdout(), "the new version has reported (or there is no update): nothing to do")
				return nil
			}
			reason := fmt.Sprintf("the new version did not report to the control plane within %s", selfupdate.TrialWindow)
			if err := a.restoreState(dir); err != nil {
				return err
			}
			if err := selfupdate.Rollback(dir, exe, reason); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "the previous binary is back; restarting the daemon")
			if out, err := runSystemTool(cmd.Context(), "systemctl", "restart", "--", agentapi.OwnUnit); err != nil {
				return fmt.Errorf("restart %s: %w: %s", agentapi.OwnUnit, err, out)
			}
			return nil
		},
	}
}

// updatePaths are the directory of the update and the path of the binary the helper acts on: this one, or when it is
// the previous binary run by the guard, the one it was kept from.
func (a *cliApp) updatePaths() (dir, exe string, err error) {
	cfg, err := a.readConfig()
	if err != nil {
		return "", "", err
	}
	exe, err = executablePath()
	if err != nil {
		return "", "", fmt.Errorf("find this binary: %w", err)
	}
	exe = strings.TrimSuffix(exe, ".prev")
	if exe == "" {
		return "", "", errors.New("cannot tell which binary to act on")
	}
	return selfupdate.Dir(cfg.StorePath), exe, nil
}

// snapshotState keeps a copy of the state database next to the note of the update. The database is only copied if
// there is one: a first start has none.
func snapshotState(ctx context.Context, storePath, dir string) error {
	if _, err := os.Stat(storePath); err != nil {
		return nil
	}
	snapshot := filepath.Join(dir, selfupdate.SnapshotName)
	_ = os.Remove(snapshot)
	return store.Snapshot(ctx, storePath, snapshot)
}

// restoreState puts the copy of the state database back when the new version has changed its schema: the previous
// binary refuses a database written by a newer one, so without the copy a rollback would leave an agent that does not start.
// What the new version recorded since the install is lost; the previous version running again is worth more.
func (a *cliApp) restoreState(dir string) error {
	cfg, err := a.readConfig()
	if err != nil {
		return err
	}
	snapshot := filepath.Join(dir, selfupdate.SnapshotName)
	if _, err := os.Stat(snapshot); err != nil {
		return nil // no copy: the install did not make one, or it was dropped
	}
	before, err := store.SchemaVersion(snapshot)
	if err != nil {
		return err
	}
	now, err := store.SchemaVersion(cfg.StorePath)
	if err != nil || now <= before {
		return err
	}
	slog.Warn("self-update: the new version changed the schema of the state database; the copy made before the install is put back", "schema", now, "back_to", before)
	return store.Restore(snapshot, cfg.StorePath)
}
