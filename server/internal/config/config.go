// Package config is server.yaml, the settings of the control plane. It holds no secret: the passwords
// and keys live in the state directory, and the password only as a hash in the database.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"sentinel/pkg/notify"
	"sentinel/pkg/pki"
	"sentinel/pkg/yamlenv"
)

// Defaults.
const (
	DefaultPath         = "/etc/sentinel/server.yaml"
	DefaultStateDir     = "/var/lib/sentinel-server"
	DefaultListen       = ":9320"
	DefaultCtlListen    = "127.0.0.1:9321"
	DefaultOfflineAfter = 3 * time.Minute
	DefaultCooldown     = time.Hour
	// minOfflineAfter is three reporting periods: an agent reports about every 60s, and one report that
	// is lost or late must not be enough to call it offline.
	minOfflineAfter = 3 * time.Minute
)

type Config struct {
	// Listen is where agents connect (mutual TLS). Open this port to the servers, and no other.
	Listen string `yaml:"listen"`
	// CtlListen is where sentinelctl connects. It is on this machine only by default: reach it over
	// an SSH tunnel, or bind it to a private address (WireGuard, Tailscale).
	CtlListen string `yaml:"ctl_listen"`
	// StateDir holds the database, the CA and the keys. Only the server's user may read it.
	StateDir string `yaml:"state_dir"`
	// Hosts are the names and addresses agents use to reach this server, as they appear in the URL:
	// the server's certificate is made for them, and the first one goes in the join command.
	Hosts []string `yaml:"hosts"`
	// OfflineAfter is how long an agent may be silent before it is called offline.
	OfflineAfter time.Duration `yaml:"offline_after"`
	// Notifiers are where the control plane sends its own alerts (an agent gone quiet, a certificate near
	// its end). Without any, the alerts are only logged and shown by `sentinelctl alert`.
	Notifiers notify.Settings `yaml:"notifiers"`
	// Cooldown is how often an alert that is still open is sent again.
	Cooldown time.Duration `yaml:"cooldown"`
	// Heartbeat is a URL the control plane requests on a schedule, so a service outside this machine
	// (healthchecks.io, Uptime Kuma) tells you when the control plane itself is down.
	Heartbeat notify.HeartbeatSettings `yaml:"heartbeat"`
}

// Default is the configuration with nothing set but the defaults.
func Default() Config {
	return Config{Listen: DefaultListen, CtlListen: DefaultCtlListen, StateDir: DefaultStateDir, OfflineAfter: DefaultOfflineAfter, Cooldown: DefaultCooldown}
}

// Load reads a config file. A key that is not known is an error, so a typo does not go unnoticed.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse reads the text of a config file, on top of the defaults.
func Parse(raw []byte) (*Config, error) {
	cfg := Default()
	if err := yamlenv.DecodeStrict(raw, &cfg, true); err != nil {
		return nil, err
	}
	if cfg.Heartbeat.URL != "" && cfg.Heartbeat.Every == 0 {
		cfg.Heartbeat.Every = notify.DefaultHeartbeatEvery
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate reports every problem at once.
func (c Config) Validate() error {
	var errs []error
	for key, address := range map[string]string{"listen": c.Listen, "ctl_listen": c.CtlListen} {
		if err := checkAddress(address); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
	}
	if c.Listen == c.CtlListen {
		errs = append(errs, errors.New("listen and ctl_listen must differ"))
	}
	if !filepath.IsAbs(c.StateDir) {
		errs = append(errs, fmt.Errorf("state_dir: must be an absolute path, got %q", c.StateDir))
	}
	if len(c.Hosts) == 0 {
		errs = append(errs, errors.New("hosts: at least one host name or address is required"))
	}
	for _, host := range c.Hosts {
		if host == "" || net.ParseIP(host) == nil && !validHostName(host) {
			errs = append(errs, fmt.Errorf("hosts: %q is not a host name or an IP address", host))
		}
	}
	if c.OfflineAfter < minOfflineAfter {
		errs = append(errs, fmt.Errorf("offline_after: must be at least %s", minOfflineAfter))
	}
	if c.Cooldown < time.Minute {
		errs = append(errs, errors.New("cooldown: must be at least 1m"))
	}
	if err := c.Notifiers.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.Heartbeat.Validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// checkAddress accepts host:port with a port from 1 to 65535 (the host may be empty: all interfaces).
func checkAddress(address string) error {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%q must be host:port, e.g. 127.0.0.1:9321", address)
	}
	if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("%q has no port from 1 to 65535", address)
	}
	return nil
}

// validHostName is a conservative check of a DNS name: labels of letters, digits and hyphens.
func validHostName(host string) bool {
	if len(host) > 253 {
		return false
	}
	labelLength := 0
	for i := 0; i < len(host); i++ {
		switch c := host[i]; {
		case c == '.':
			if labelLength == 0 || host[i-1] == '-' {
				return false
			}
			labelLength = 0
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			labelLength++
		case c == '-' && labelLength > 0:
			labelLength++
		default:
			return false
		}
		if labelLength > 63 {
			return false
		}
	}
	return labelLength > 0 && host[len(host)-1] != '-'
}

// Port is the port of Listen.
func (c Config) Port() string {
	_, port, _ := net.SplitHostPort(c.Listen)
	return port
}

// JoinURL is the address an agent is given to join.
func (c Config) JoinURL() string {
	return "https://" + net.JoinHostPort(c.Hosts[0], c.Port())
}

// Files are the files the server keeps in its state directory.
type Files struct{ Dir string }

func (c Config) Files() Files { return Files{Dir: c.StateDir} }

func (f Files) DB() string         { return filepath.Join(f.Dir, "server.db") }
func (f Files) CACert() string     { return filepath.Join(f.Dir, "ca.pem") }
func (f Files) CAKey() string      { return filepath.Join(f.Dir, "ca-key.pem") }
func (f Files) ServerCert() string { return filepath.Join(f.Dir, "server.pem") }
func (f Files) ServerKey() string  { return filepath.Join(f.Dir, "server-key.pem") }

// All lists them, the one that says "initialized" (the CA certificate) last.
func (f Files) All() []string {
	return []string{f.DB(), f.CAKey(), f.ServerKey(), f.ServerCert(), f.CACert()}
}

// Write saves the config as a file anybody may read (it has no secret), unless one is already there.
func Write(path string, cfg Config) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	text := fmt.Sprintf(`# Sentinel control plane. Docs: docs/rfc/0002-agent-control-plane.md
# Agents connect here over mutual TLS; open this port to your servers and to nobody else.
listen: %q
# sentinelctl connects here. Keep it on this machine and use an SSH tunnel, or bind it to a private address.
ctl_listen: %q
# The database, the CA and the keys. Back it up: it holds the CA key.
state_dir: %q
# The names and addresses agents use to reach this server (the first one goes in the join command).
hosts:
`, cfg.Listen, cfg.CtlListen, cfg.StateDir)
	for _, host := range cfg.Hosts {
		text += "  - " + strconv.Quote(host) + "\n"
	}
	text += fmt.Sprintf("# An agent that has not reported for this long is offline.\noffline_after: %s\n", cfg.OfflineAfter)
	text += fmt.Sprintf(`# Where alerts go, and how often one that stays open is sent again. Write secrets as ${VAR} and keep
# the values in the environment file. Without a notifier the alerts are logged and shown by sentinelctl alert.
# notifiers:
#   telegram: {token: ${TELEGRAM_TOKEN}, chat_id: "123456"}
#   slack: {webhook: ${SLACK_WEBHOOK}}
cooldown: %s
# Request this URL every few minutes so an outside service can tell you when the control plane is down.
# heartbeat: {url: ${HEARTBEAT_URL}, every: 5m}
`, cfg.Cooldown)
	return pki.WritePublic(path, []byte(text))
}
