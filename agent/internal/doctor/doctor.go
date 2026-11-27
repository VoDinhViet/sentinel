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
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/engine"
	"sentinel/agent/internal/restic"
	"sentinel/agent/internal/store"
	"sentinel/agent/internal/transport"
	"sentinel/pkg/notify"
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

// ConfigAccess reports who can use the config file. The daemon runs as root, so a file another user can
// write is a way to run commands as root, and one every user can read may hold passwords. Group write is
// only a warning: the default umask of Debian and Ubuntu gives a private group write access to what its
// user creates. A file that cannot be read is left to the loader to report.
func ConfigAccess(path string) Finding {
	finding := Finding{Name: "config", Info: path}
	info, err := os.Stat(path)
	if err != nil {
		return finding
	}
	mode := info.Mode().Perm()
	switch {
	case mode&0o002 != 0:
		finding.Err = fmt.Errorf("every user can change this file (mode %04o), and the daemon runs it as root", mode)
	case mode&0o020 != 0:
		finding.Err, finding.Warn = fmt.Errorf("the group can change this file (mode %04o), and the daemon runs it as root", mode), true
	case mode&0o004 != 0:
		finding.Err, finding.Warn = fmt.Errorf("every user can read this file (mode %04o) and it may hold passwords", mode), true
	default:
		return finding
	}
	finding.Hint = "run: chmod 640 " + path
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
	findings := []Finding{ConfigAccess(configPath), Locale(os.Getenv)}
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

	findings = append(findings, repositoryFindings(ctx, cfg)...)

	notifierCount := len(notify.FromConfig(cfg.Notifiers))
	notifierFinding := Finding{Name: "notifiers", Info: fmt.Sprintf("%d configured (test with: sentinel notify test)", notifierCount)}
	if notifierCount == 0 {
		notifierFinding.Err, notifierFinding.Warn = errors.New("none configured: alerts will only be logged"), true
		notifierFinding.Hint = "add telegram, smtp, discord or slack under notifiers: in the config"
	}
	findings = append(findings, notifierFinding, heartbeatFinding(cfg))
	if cfg.ControlPlane.Enabled() {
		findings = append(findings, controlPlaneFinding(ctx, cfg))
		findings = append(findings, allowToolFindings(cfg.ControlPlane.Allow)...)
	}
	if blocks := cfg.Deprecated(); len(blocks) > 0 {
		findings = append(findings, deprecatedFinding(blocks))
	}
	return findings
}

// repositoryFindings checks every repository at the same time, in the order of their names: one that cannot be
// reached takes up to its whole timeout to say so, and the ones after it must not wait for that or run out of
// the time the caller gave the whole check.
func repositoryFindings(ctx context.Context, cfg *config.Config) []Finding {
	runner := &backup.Runner{Config: cfg}
	names := slices.Sorted(maps.Keys(cfg.Repositories))
	findings := make([]Finding, len(names))
	var checks sync.WaitGroup
	for i, name := range names {
		checks.Go(func() { findings[i] = repositoryFinding(ctx, runner, name) })
	}
	checks.Wait()
	return findings
}

func repositoryFinding(ctx context.Context, runner *backup.Runner, name string) Finding {
	repoFinding := Finding{Name: "repository " + name}
	client, _ := runner.Restic(name)
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	initialized, err := client.Exists(checkCtx)
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
	return repoFinding
}

// controlPlaneFinding checks that the identity from `sentinel agent join` is there and the control plane
// takes this agent for what it is. An unreachable control plane is a warning: the agent works without
// it, it only goes unwatched.
func controlPlaneFinding(ctx context.Context, cfg *config.Config) Finding {
	dir := cfg.ControlPlaneDir()
	finding := Finding{Name: "control_plane"}
	if _, err := transport.LoadIdentity(dir); err != nil {
		finding.Err = err
		finding.Hint = "join the control plane first: sentinelctl server add <name>, then run the command it prints on this server"
		return finding
	}
	if err := dirWritable(dir); err != nil {
		finding.Err = fmt.Errorf("%s cannot be written, so the certificate cannot be renewed: %w", dir, err)
		finding.Hint = "the unit's ProtectSystem=full makes /etc read-only: keep the identity under the state directory (control_plane.dir)"
		return finding
	}
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	who, err := transport.WhoAmI(callCtx, dir, cfg.ControlPlane.URL)
	switch {
	case transport.IsStatus(err, 401):
		finding.Err = fmt.Errorf("the control plane at %s refuses this agent's certificate: %w", cfg.ControlPlane.URL, err)
		finding.Hint = "the server was revoked or its certificate ended: sentinelctl server add <name>, then sentinel agent join --force"
	case err != nil:
		finding.Warn = true
		finding.Err = fmt.Errorf("cannot reach the control plane at %s: %w", cfg.ControlPlane.URL, err)
		finding.Hint = "check the url, the network and that sentinel-server is running; this agent keeps working without it"
	default:
		finding.Info = fmt.Sprintf("%s knows this agent as %q; certificate ends %s; tasks allowed: %s", cfg.ControlPlane.URL, who.Name,
			who.NotAfter.Local().Format("2006-01-02"), allowedText(cfg.ControlPlane.Allow))
	}
	return finding
}

// allowToolFindings warns when the allowlist names units or containers and what the tasks need to touch them
// is not here: a task would only fail, and the person who wrote the list would not know why.
func allowToolFindings(allow config.Allow) []Finding {
	var findings []Finding
	if len(allow.Services) > 0 {
		if _, err := exec.LookPath("systemctl"); err != nil {
			findings = append(findings, Finding{Name: "allow.services", Warn: true, Err: errors.New("systemctl was not found, so service tasks will fail"),
				Hint: "this machine has no systemd (a container, a minimal image): remove control_plane.allow.services"})
		}
	}
	if len(allow.Containers) > 0 {
		if info, err := os.Stat(allow.Socket()); err != nil || info.Mode()&os.ModeSocket == 0 {
			findings = append(findings, Finding{Name: "allow.containers", Warn: true, Err: fmt.Errorf("%s is not a Docker socket, so container tasks will fail", allow.Socket()),
				Hint: "start Docker, or set control_plane.allow.docker_socket to the right path"})
		}
	}
	findings = append(findings, osUpdateFindings(allow.OSUpdates)...)
	findings = append(findings, selfUpdateFindings(allow.SelfUpdate)...)
	return findings
}

// osToolDirs are the places the executor looks for apt-get and systemd-run (it ignores the PATH).
var osToolDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// osUpdateFindings warns when os_updates is on and the tools its tasks run are not where the executor looks.
func osUpdateFindings(mode string) []Finding {
	needs := map[string][]string{config.OSUpdatesCheck: {"apt-get"}, config.OSUpdatesApply: {"apt-get", "systemd-run"}}[mode]
	var findings []Finding
	for _, tool := range needs {
		if !slices.ContainsFunc(osToolDirs, func(dir string) bool {
			info, err := os.Stat(filepath.Join(dir, tool))
			return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
		}) {
			findings = append(findings, Finding{Name: "allow.os_updates", Warn: true, Err: fmt.Errorf("%s was not found, so os_updates tasks will fail", tool),
				Hint: "only Debian and Ubuntu (apt) are supported: set control_plane.allow.os_updates to off"})
		}
	}
	return findings
}

// selfUpdateFindings warns when agent.update is on and systemd-run, which puts the new binary in place outside the
// sandbox of the daemon, is not where the executor looks.
func selfUpdateFindings(self config.SelfUpdate) []Finding {
	if !self.Enabled() || slices.ContainsFunc(osToolDirs, func(dir string) bool {
		info, err := os.Stat(filepath.Join(dir, "systemd-run"))
		return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
	}) {
		return nil
	}
	return []Finding{{Name: "allow.self_update", Warn: true, Err: errors.New("systemd-run was not found, so agent.update tasks will fail"),
		Hint: "the new binary is put in place by a transient unit: remove control_plane.allow.self_update on a machine without systemd"}}
}

// allowedText is the actions the allowlist lets the control plane ask for, "none" when it is off.
func allowedText(allow config.Allow) string {
	if actions := allow.Actions(); len(actions) > 0 {
		return strings.Join(actions, ", ")
	}
	return "none (control_plane.allow is off)"
}

// dirWritable says whether a file can be made in dir: a certificate is renewed by writing new files there.
func dirWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	return errors.Join(probe.Close(), os.Remove(name))
}

// deprecatedFinding says which blocks of the config do nothing any more: the daemon listens on no port and
// does not watch other servers, the control plane does that.
func deprecatedFinding(blocks []string) Finding {
	return Finding{
		Name: "deprecated", Warn: true,
		Err:  fmt.Errorf("%s: ignored, the daemon no longer listens on a port or watches other servers", strings.Join(blocks, ", ")),
		Hint: "remove those blocks from the config; the control plane (sentinelctl) shows every server and alerts when one goes silent",
	}
}

// heartbeatFinding warns when nothing outside the server would notice the daemon stopping: its own
// alerts cannot report that.
func heartbeatFinding(cfg *config.Config) Finding {
	if cfg.Heartbeat.URL != "" {
		return Finding{Name: "heartbeat", Info: fmt.Sprintf("requested every %s", cfg.Heartbeat.Every)}
	}
	return Finding{
		Name: "heartbeat", Warn: true,
		Err:  errors.New("not configured: nothing notices when the daemon stops"),
		Hint: "set heartbeat.url to a healthchecks.io or Uptime Kuma push URL",
	}
}
