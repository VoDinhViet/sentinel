package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/engine"
	"sentinel/internal/notify"
	"sentinel/internal/restic"
	"sentinel/internal/store"
)

var minRestic = [3]int{0, 17, 0} // first release with --stdin-from-command

type finding struct {
	name string
	err  error
	info string
	warn bool   // err is a warning: reported, but doctor still passes
	hint string // how to fix it, shown by the interactive screen
}

// checkBinaries verifies the external tools the config needs are installed.
func checkBinaries(ctx context.Context, cfg *config.Config) []finding {
	var out []finding
	needRestic := len(cfg.Backups) > 0
	needRclone := false
	for _, r := range cfg.Repositories {
		if strings.HasPrefix(r.URL, "rclone:") {
			needRclone = true
		}
	}

	if needRestic {
		f := finding{name: "restic"}
		v, err := restic.Version(ctx, "")
		switch {
		case err != nil:
			f.err = fmt.Errorf("not usable: %w", err)
			f.hint = "install restic >= 0.17 (https://restic.net, or: sudo apt install restic)"
		case versionLess(v, minRestic):
			f.err = fmt.Errorf("version %d.%d.%d is too old, need >= %d.%d.%d", v[0], v[1], v[2], minRestic[0], minRestic[1], minRestic[2])
			f.hint = "upgrade restic to 0.17 or newer"
		default:
			f.info = fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
		}
		out = append(out, f)
	}
	var tools []string
	if needRclone {
		tools = append(tools, "rclone")
	}
	tools = append(tools, engine.ToolsFor(cfg.Backups)...)
	for _, tool := range tools {
		f := finding{name: tool}
		if path, err := exec.LookPath(tool); err != nil {
			f.err = errors.New("not found in PATH")
			f.hint = "install " + tool + " and make sure it is on PATH (see the install section of the README)"
		} else {
			f.info = path
		}
		out = append(out, f)
	}
	return out
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func printFindings(w io.Writer, fs []finding) (failed int) {
	for _, f := range fs {
		if f.err != nil && f.warn {
			fmt.Fprintf(w, "  WARN  %-28s %v\n", f.name, f.err)
		} else if f.err != nil {
			failed++
			fmt.Fprintf(w, "  FAIL  %-28s %v\n", f.name, f.err)
		} else {
			fmt.Fprintf(w, "  ok    %-28s %s\n", f.name, f.info)
		}
	}
	return failed
}

// runDoctor checks the config's tools, store, repositories and notifiers. It is
// what `sentinel doctor` prints and what the Doctor tab of `sentinel manage` shows.
func runDoctor(ctx context.Context, cfg *config.Config, configPath string, openStore func(*config.Config) (*store.Store, error)) []finding {
	findings := []finding{{name: "config", info: configPath}}
	findings = append(findings, checkBinaries(ctx, cfg)...)

	st, err := openStore(cfg)
	if err == nil {
		st.Close()
	}
	storeFinding := finding{name: "store", err: err, info: cfg.Store}
	if err != nil {
		storeFinding.hint = "check that the directory of " + cfg.Store + " exists and is writable"
	}
	findings = append(findings, storeFinding)

	runner := &backup.Runner{Config: cfg}
	for _, name := range slices.Sorted(maps.Keys(cfg.Repositories)) {
		repoFinding := finding{name: "repository " + name}
		client, _ := runner.Restic(name)
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		initialized, err := client.Exists(checkCtx)
		cancel()
		switch {
		case err != nil:
			repoFinding.err = err
			repoFinding.hint = "check the repository url, its credentials and the network"
		case !initialized:
			repoFinding.err = errors.New("not initialized (run: sentinel backup init <job>)")
			repoFinding.hint = "run: sentinel backup init <job> (once per repository)"
		default:
			repoFinding.info = "reachable"
		}
		findings = append(findings, repoFinding)
	}

	channels := len(notify.FromConfig(cfg.Notifiers))
	notifierFinding := finding{name: "notifiers", info: fmt.Sprintf("%d configured (test with: sentinel notify test)", channels)}
	if channels == 0 {
		notifierFinding.err, notifierFinding.warn = errors.New("none configured: alerts will only be logged"), true
		notifierFinding.hint = "add telegram, smtp, discord or slack under notifiers: in the config"
	}
	return append(findings, notifierFinding)
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, tools, repositories and storage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			cfg, err := a.loadConfig()
			if err != nil {
				fmt.Fprintf(w, "  FAIL  %-28s %v\n", strings.TrimSpace("config "+a.configPath), err)
				return errors.New("doctor found problems")
			}
			if printFindings(w, runDoctor(cmd.Context(), cfg, a.configPath, a.openStore)) > 0 {
				return errors.New("doctor found problems")
			}
			return nil
		},
	}
}
