// Package doctor checks that a Sentinel install can work: the config file, the external
// tools, the store, the repositories and the notifiers. It is what `sentinel doctor`
// prints and what the Doctor tab of `sentinel manage` shows.
package doctor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/engine"
	"sentinel/agent/internal/fleet"
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
	}
	if cfg.API.Listen != "" {
		findings = append(findings, apiFinding(cfg))
	}
	if cfg.Fleet.Config != "" {
		findings = append(findings, fleetFinding(cfg.Fleet))
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
		finding.Info = fmt.Sprintf("%s knows this agent as %q; certificate ends %s", cfg.ControlPlane.URL, who.Name, who.NotAfter.Local().Format("2006-01-02"))
	}
	return finding
}

// apiFinding warns when the API can be reached from other machines: the token travels in clear text.
// With a certificate it checks the certificate instead.
func apiFinding(cfg *config.Config) Finding {
	if cfg.API.TLS() {
		return apiTLSFinding(cfg.API, time.Now())
	}
	if cfg.API.Loopback() {
		return Finding{Name: "api", Info: "listens on " + cfg.API.Listen + " (this machine only)"}
	}
	return Finding{
		Name: "api", Warn: true,
		Err:  fmt.Errorf("listens on %s, which other machines can reach, and the token is sent without TLS", cfg.API.Listen),
		Hint: "listen on 127.0.0.1 and put a TLS reverse proxy or an SSH tunnel in front, or firewall the port",
	}
}

// fleetFinding checks the fleet file the daemon is to watch. A file it cannot read means no server is
// watched, which the daemon says in an alert once it runs: here it is a warning before that.
func fleetFinding(settings config.Fleet) Finding {
	cfg, err := fleet.Load(settings.Config)
	if err != nil {
		return Finding{Name: "fleet", Warn: true, Err: err, Hint: "fix the fleet file: until then the daemon watches no server"}
	}
	if open := serversOverPlainHTTP(cfg.Servers); len(open) > 0 {
		return Finding{
			Name: "fleet", Warn: true,
			Err:  fmt.Errorf("%s: asked over http:// on another machine, so the token is sent without TLS", strings.Join(open, ", ")),
			Hint: "give that server api.tls_cert and api.tls_key and use https://, or reach it through an SSH tunnel",
		}
	}
	return Finding{Name: "fleet", Info: fmt.Sprintf("watching %d servers every %s, alert after %d failed rounds", len(cfg.Servers), settings.Every, settings.FailAfter)}
}

// serversOverPlainHTTP names the servers whose token travels in clear text to another machine: an
// http:// address that is not this machine's own.
func serversOverPlainHTTP(servers []fleet.Server) []string {
	var open []string
	for _, server := range servers {
		address, err := url.Parse(server.URL)
		if err == nil && address.Scheme == "http" && !isThisMachine(address.Hostname()) {
			open = append(open, server.Name)
		}
	}
	return open
}

func isThisMachine(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// certificateWarnDays is how long before it expires a certificate of the API is reported.
const certificateWarnDays = 14

// apiTLSFinding checks what the daemon will do at start: load the certificate and its key. It fails
// for a pair that cannot be loaded or a certificate that has expired, and warns when the
// certificate expires soon or its key can be read by other users (both are said when both hold).
func apiTLSFinding(settings config.API, now time.Time) Finding {
	pair, err := tls.LoadX509KeyPair(settings.TLSCert, settings.TLSKey)
	if err != nil {
		return Finding{Name: "api", Err: fmt.Errorf("tls_cert and tls_key cannot be loaded: %w", err), Hint: "give the PEM files of a certificate and its private key"}
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Finding{Name: "api", Err: fmt.Errorf("tls_cert is not a certificate: %w", err)}
	}
	expiry := leaf.NotAfter.Format("2006-01-02")
	left := leaf.NotAfter.Sub(now)
	if left <= 0 {
		return Finding{Name: "api", Err: fmt.Errorf("the certificate of the API expired on %s", expiry), Hint: "renew it and restart the daemon"}
	}
	// Every warning is shown: one must not hide the other.
	var problems, hints []string
	if left < certificateWarnDays*24*time.Hour {
		problems = append(problems, "the certificate of the API expires on "+expiry)
		hints = append(hints, "renew it and restart the daemon: it is read at start")
	}
	if info, err := os.Stat(settings.TLSKey); err == nil && info.Mode().Perm()&0o077 != 0 {
		problems = append(problems, fmt.Sprintf("%s, the private key of the API, can be read by other users", settings.TLSKey))
		hints = append(hints, "run chmod 600 on it")
	}
	if len(problems) > 0 {
		return Finding{Name: "api", Warn: true, Err: errors.New(strings.Join(problems, "; ")), Hint: strings.Join(hints, "; ")}
	}
	return Finding{Name: "api", Info: fmt.Sprintf("HTTPS on %s, the certificate expires on %s", settings.Listen, expiry)}
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
