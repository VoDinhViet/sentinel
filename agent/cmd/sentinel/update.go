package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// guardUnit is the transient service that puts the previous binary back when the new version never gets as far as doing
// it itself (a config it cannot read, a store it cannot open, a hang): the install starts it before the new binary exists.
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
			// Nothing is changed until the update is known to be good: the copy of the database and the guard come after.
			if _, _, err := selfupdate.Check(dir, key); err != nil {
				return err
			}
			if err := snapshotState(cmd.Context(), cfg.StorePath, dir); err != nil {
				return fmt.Errorf("keep a copy of the state database for a rollback: %w", err)
			}
			// The guard before the install: an install that cannot be watched is not done.
			if err := armGuard(cmd.Context(), exe, a.configPath); err != nil {
				return fmt.Errorf("start the guard that goes back if the new version is silent: %w", err)
			}
			if err := selfupdate.Install(dir, exe, key); err != nil {
				_, _ = runSystemTool(cmd.Context(), "systemctl", "stop", "--", guardUnit+".service")
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "the new binary is in place; the previous one is kept as", exe+".prev")
			return nil
		},
	}
}

// armGuard starts the service that runs the previous binary's update-guard.
func armGuard(ctx context.Context, exe, configPath string) error {
	absolute, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}
	_, err = runSystemTool(ctx, "systemd-run", "--unit="+guardUnit, "--collect", "--quiet", "--", exe+".prev", "--config", absolute, "update-guard")
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
			if err := a.goBack(dir, exe, selfupdate.WhyGoBack(dir)); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "the previous binary is back")
			return nil
		},
	}
}

// goBack puts the previous binary back, and the copy of the state database if the new version moved its schema on. The
// binary is what matters: a copy that cannot be put back is logged, and the rollback goes on.
func (a *cliApp) goBack(dir, exe, reason string) error {
	if err := selfupdate.CanRollback(dir, exe); err != nil {
		return err
	}
	if err := a.restoreState(dir); err != nil {
		slog.Error("self-update: the state database could not be put back; the previous binary is put back anyway", "err", err)
	}
	return selfupdate.Rollback(dir, exe, reason)
}

// The pace of the guard, and how long it waits; tests shorten them.
var (
	guardEvery    = 30 * time.Second
	guardPatience = selfupdate.TrialWindow + time.Minute // how long the new version is given once the old daemon is gone
	guardMaxWait  = 4 * time.Hour                        // longer than the daemon waits for running backups; then it stops waiting for it
)

// updateGuardCmd is run by the service the install started, as the previous binary. It waits while the old daemon is
// still finishing its backups (the new binary has not started yet, and has not been given its trial), gives the new
// version the trial window from then on, and if the note still says "on trial" puts the previous binary back and
// restarts the daemon, whatever state the new version is in.
func (a *cliApp) updateGuardCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "update-guard",
		Short:  "Quay về binary cũ nếu bản mới chưa báo cáo (daemon gọi, không chạy tay)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, exe, err := a.updatePaths()
			if err != nil {
				return err
			}
			var trialFrom time.Time
			for {
				marker, found, _ := selfupdate.ReadMarker(dir)
				if !found || marker.State != selfupdate.Installed {
					fmt.Fprintln(cmd.OutOrStdout(), "the new version has reported (or there is no update): nothing to do")
					return nil
				}
				now := time.Now()
				if now.Sub(marker.At) < guardMaxWait && oldDaemonRuns(cmd.Context(), marker.At) {
					trialFrom = time.Time{}
				} else if trialFrom.IsZero() {
					trialFrom = now
				}
				if !trialFrom.IsZero() && now.Sub(trialFrom) >= guardPatience {
					break
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(guardEvery):
				}
			}
			reason := fmt.Sprintf("the new version did not report to the control plane within %s", selfupdate.TrialWindow)
			if err := a.goBack(dir, exe, reason); err != nil {
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

// oldDaemonRuns says the process of the daemon was started before the install: it is the old one, still finishing its
// backups before it stops. A daemon that cannot be found (it is between two starts) is not the old one.
var oldDaemonRuns = func(ctx context.Context, installedAt time.Time) bool {
	out, err := runSystemTool(ctx, "systemctl", "show", "--property=MainPID", "--value", "--", agentapi.OwnUnit)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || pid <= 0 {
		return false
	}
	started, err := processStart(pid)
	return err == nil && !started.After(installedAt)
}

// processStart is when a process started, from /proc: the time since boot in clock ticks (100 a second on Linux), and
// the time of the boot.
func processStart(pid int) (time.Time, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, err
	}
	ticks, err := startTicks(string(stat))
	if err != nil {
		return time.Time{}, err
	}
	system, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(system), "\n") {
		if text, found := strings.CutPrefix(line, "btime "); found {
			boot, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(boot, 0).Add(time.Duration(ticks) * 10 * time.Millisecond), nil
		}
	}
	return time.Time{}, errors.New("/proc/stat has no btime")
}

// startTicks reads the 22nd field of /proc/<pid>/stat. The name in the second field may hold spaces and brackets, so the
// fields are counted from the last closing bracket.
func startTicks(stat string) (int64, error) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, errors.New("unexpected /proc stat")
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return 0, errors.New("unexpected /proc stat")
	}
	return strconv.ParseInt(fields[19], 10, 64)
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

// snapshotState keeps a copy of the state database next to the note of the update. There is none to copy on a first start.
func snapshotState(ctx context.Context, storePath, dir string) error {
	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	snapshot := filepath.Join(dir, selfupdate.SnapshotName)
	_ = os.Remove(snapshot)
	return store.Snapshot(ctx, storePath, snapshot)
}

// restoreState puts the copy of the state database back when the new version has changed its schema: the previous
// binary refuses a database written by a newer one, so without the copy a rollback would leave an agent that does not start.
// What was recorded since the install is lost from the database (the file it was in is kept as state.db.rolled-back); the
// previous version running again is worth more.
func (a *cliApp) restoreState(dir string) error {
	cfg, err := a.readConfig()
	if err != nil {
		return err
	}
	snapshot := filepath.Join(dir, selfupdate.SnapshotName)
	if _, err := os.Lstat(snapshot); errors.Is(err, fs.ErrNotExist) {
		return nil // no copy: the install did not make one, or it was dropped
	} else if err != nil {
		return err
	}
	before, err := store.SchemaVersion(snapshot)
	if err != nil {
		return err
	}
	if before < 1 {
		return errors.New("the copy of the state database is empty")
	}
	now, err := store.SchemaVersion(cfg.StorePath)
	if err == nil && now <= before {
		return nil
	}
	slog.Warn("self-update: the new version changed the schema of the state database, or it cannot be read; the copy made before the install is put back",
		"schema", now, "back_to", before, "read_error", err)
	return store.Restore(snapshot, cfg.StorePath)
}
