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
	"sentinel/internal/notify"
)

var minRestic = [3]int{0, 17, 0} // first release with --stdin-from-command

type finding struct {
	name string
	err  error
	info string
	warn bool // err is a warning: reported, but doctor still passes
}

// checkBinaries verifies the external tools the config needs are installed.
func checkBinaries(ctx context.Context, cfg *config.Config) []finding {
	var out []finding
	needRestic := len(cfg.Backups) > 0
	needRclone, needPgDump := false, false
	for _, r := range cfg.Repositories {
		if strings.HasPrefix(r.URL, "rclone:") {
			needRclone = true
		}
	}
	for _, b := range cfg.Backups {
		if b.Type == config.BackupPostgres {
			needPgDump = true
		}
	}

	if needRestic {
		f := finding{name: "restic"}
		v, err := backup.Version(ctx, "")
		switch {
		case err != nil:
			f.err = fmt.Errorf("not usable: %w", err)
		case versionLess(v, minRestic):
			f.err = fmt.Errorf("version %d.%d.%d is too old, need >= %d.%d.%d", v[0], v[1], v[2], minRestic[0], minRestic[1], minRestic[2])
		default:
			f.info = fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
		}
		out = append(out, f)
	}
	for _, bin := range []struct {
		name   string
		needed bool
	}{{"rclone", needRclone}, {"pg_dump", needPgDump}} {
		if !bin.needed {
			continue
		}
		f := finding{name: bin.name}
		if path, err := exec.LookPath(bin.name); err != nil {
			f.err = errors.New("not found in PATH")
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

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, tools, repositories and storage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			cfg, err := a.loadConfig()
			if err != nil {
				fmt.Fprintf(w, "  FAIL  %-28s %v\n", "config "+a.configPath, err)
				return errors.New("doctor found problems")
			}
			fs := []finding{{name: "config", info: a.configPath}}
			fs = append(fs, checkBinaries(cmd.Context(), cfg)...)

			st, err := a.openStore(cfg)
			if err == nil {
				st.Close()
			}
			fs = append(fs, finding{name: "store", err: err, info: cfg.Store})

			runner := &backup.Runner{Config: cfg}
			names := slices.Sorted(maps.Keys(cfg.Repositories))
			for _, name := range names {
				f := finding{name: "repository " + name}
				rc, _ := runner.Restic(name)
				ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
				ok, err := rc.Exists(ctx)
				cancel()
				switch {
				case err != nil:
					f.err = err
				case !ok:
					f.err = errors.New("not initialized (run: sentinel backup init <job>)")
				default:
					f.info = "reachable"
				}
				fs = append(fs, f)
			}

			n := len(notify.FromConfig(cfg.Notifiers))
			nf := finding{name: "notifiers", info: fmt.Sprintf("%d configured (test with: sentinel notify test)", n)}
			if n == 0 {
				nf.err, nf.warn = errors.New("none configured: alerts will only be logged"), true
			}
			fs = append(fs, nf)

			if printFindings(w, fs) > 0 {
				return errors.New("doctor found problems")
			}
			return nil
		},
	}
}
