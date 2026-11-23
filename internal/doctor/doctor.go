// Package doctor checks that a Sentinel install can work: the config file, the external
// tools, the store, the repositories and the notifiers. It is what `sentinel doctor`
// prints and what the Doctor tab of `sentinel manage` shows.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/engine"
	"sentinel/internal/notify"
	"sentinel/internal/restic"
	"sentinel/internal/store"
)

var minRestic = [3]int{0, 17, 0} // first release with --stdin-from-command

// Finding is the result of one check.
type Finding struct {
	Name string
	Err  error
	Info string
	Warn bool   // Err is a warning: reported, but doctor still passes
	Hint string // how to fix it, shown by the interactive screen
}

// Locale warns when the character set of the environment is not UTF-8: the screens of
// `sentinel manage` are Vietnamese and draw frames, so on a server with the POSIX locale
// (the default of many VPS images) the text and the lines come out broken. The terminal
// program's font matters too, but only the client can fix that.
func Locale(getenv func(string) string) Finding {
	finding := Finding{Name: "locale"}
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} { // the first one that is set wins
		if value := getenv(name); value != "" {
			finding.Info = name + "=" + value
			if lower := strings.ToLower(value); strings.Contains(lower, "utf-8") || strings.Contains(lower, "utf8") {
				return finding
			}
			break
		}
	}
	finding.Err, finding.Warn = errors.New("the locale is not UTF-8: Vietnamese text and frames may be shown broken"), true
	finding.Hint = "run: export LANG=C.UTF-8 (or en_US.UTF-8), and use a terminal font with Vietnamese letters, e.g. JetBrains Mono"
	return finding
}

// Tools verifies the external tools the config needs are installed.
func Tools(ctx context.Context, cfg *config.Config) []Finding {
	var findings []Finding
	needRestic := len(cfg.Jobs) > 0
	needRclone := false
	for _, repo := range cfg.Repositories {
		if strings.HasPrefix(repo.URL, "rclone:") {
			needRclone = true
		}
	}

	if needRestic {
		finding := Finding{Name: "restic"}
		ver, err := restic.Version(ctx, "")
		switch {
		case err != nil:
			finding.Err = fmt.Errorf("not usable: %w", err)
			finding.Hint = "install restic >= 0.17 (https://restic.net, or: sudo apt install restic)"
		case versionLess(ver, minRestic):
			finding.Err = fmt.Errorf("version %d.%d.%d is too old, need >= %d.%d.%d", ver[0], ver[1], ver[2], minRestic[0], minRestic[1], minRestic[2])
			finding.Hint = "upgrade restic to 0.17 or newer"
		default:
			finding.Info = fmt.Sprintf("%d.%d.%d", ver[0], ver[1], ver[2])
		}
		findings = append(findings, finding)
	}
	var tools []string
	if needRclone {
		tools = append(tools, "rclone")
	}
	tools = append(tools, engine.ToolsFor(cfg.Jobs)...)
	for _, tool := range tools {
		finding := Finding{Name: tool}
		if path, err := exec.LookPath(tool); err != nil {
			finding.Err = errors.New("not found in PATH")
			finding.Hint = "install " + tool + " and make sure it is on PATH (see the install section of the README)"
		} else {
			finding.Info = path
		}
		findings = append(findings, finding)
	}
	return findings
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// Run checks the config's tools, store, repositories and notifiers.
func Run(ctx context.Context, cfg *config.Config, configPath string, openStore func(*config.Config) (*store.Store, error)) []Finding {
	findings := []Finding{{Name: "config", Info: configPath}, Locale(os.Getenv)}
	findings = append(findings, Tools(ctx, cfg)...)

	st, err := openStore(cfg)
	if err == nil {
		st.Close()
	}
	storeFinding := Finding{Name: "store", Err: err, Info: cfg.StorePath}
	if err != nil {
		storeFinding.Hint = "check that the directory of " + cfg.StorePath + " exists and is writable"
	}
	findings = append(findings, storeFinding)

	runner := &backup.Runner{Config: cfg}
	for _, name := range slices.Sorted(maps.Keys(cfg.Repositories)) {
		repoFinding := Finding{Name: "repository " + name}
		client, _ := runner.Restic(name)
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		initialized, err := client.Exists(checkCtx)
		cancel()
		switch {
		case err != nil:
			repoFinding.Err = err
			repoFinding.Hint = "check the repository url, its credentials and the network"
		case !initialized:
			repoFinding.Err = errors.New("not initialized (run: sentinel backup init <job>)")
			repoFinding.Hint = "run: sentinel backup init <job> (once per repository)"
		default:
			repoFinding.Info = "reachable"
		}
		findings = append(findings, repoFinding)
	}

	notifierCount := len(notify.FromConfig(cfg.Notifiers))
	notifierFinding := Finding{Name: "notifiers", Info: fmt.Sprintf("%d configured (test with: sentinel notify test)", notifierCount)}
	if notifierCount == 0 {
		notifierFinding.Err, notifierFinding.Warn = errors.New("none configured: alerts will only be logged"), true
		notifierFinding.Hint = "add telegram, smtp, discord or slack under notifiers: in the config"
	}
	return append(findings, notifierFinding)
}
