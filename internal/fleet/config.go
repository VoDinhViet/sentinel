// Package fleet reads how several Sentinel servers are doing, by calling the HTTP API of each daemon.
// The machine that runs it needs no config of its own: only a fleet file that names the servers.
package fleet

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sentinel/internal/config"
)

// EnvFileName is the file beside the fleet file that holds the secrets its ${VAR} references name.
const EnvFileName = "fleet.env"

// EnvPath is the variable that names the fleet file when --fleet-config is not given.
const EnvPath = "SENTINEL_FLEET_CONFIG"

// Config is the fleet file: the servers to watch, in the order they are shown.
type Config struct {
	Servers []Server `yaml:"servers"`
}

// Server is one daemon with its API on.
type Server struct {
	Name  string `yaml:"name"`           // how the server is shown, and part of the key of its alerts
	URL   string `yaml:"url"`            // base address of the API, such as https://web-01.example.com:9311
	Token string `yaml:"token" json:"-"` // api.token or, better, api.read_token of that server; never written out
	TLSCA string `yaml:"tls_ca"`         // PEM file of the authority that signed the server's certificate, when it is not a public one
}

// serverName is what a server may be called: it ends up in alert keys and on screens.
var serverName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// DefaultPath is the fleet file when --fleet-config is not given: $SENTINEL_FLEET_CONFIG, else
// sentinel/fleet.yaml in the user's config directory. It is "" when neither is known.
func DefaultPath() string {
	if path := os.Getenv(EnvPath); path != "" {
		return path
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "sentinel", "fleet.yaml")
}

// Load reads and checks the fleet file. A ${VAR} in it is replaced from the environment or from
// fleet.env beside the file. A token written out in the file is refused while other users can
// read the file.
func Load(path string) (*Config, error) {
	raw, err := readFleetFile(path)
	if err != nil {
		return nil, err
	}
	if err := refusePlainTokensInOpenFile(path, raw); err != nil {
		return nil, err
	}
	if err := loadSecrets(path, raw); err != nil {
		return nil, err
	}
	var cfg Config
	if err := config.DecodeStrict(raw, &cfg, true); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func readFleetFile(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("no fleet file: pass --fleet-config or set $%s", EnvPath)
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("fleet file %s does not exist: create it with the servers to watch, as the README section \"Nhiều server\" shows", path)
	}
	return raw, err
}

// refusePlainTokensInOpenFile stops a token that is written in the file, not given as ${VAR},
// from sitting in a file that group or others can read.
func refusePlainTokensInOpenFile(path string, raw []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	var written Config
	if err := config.DecodeStrict(raw, &written, false); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, server := range written.Servers {
		if server.Token != "" && !strings.Contains(server.Token, "${") {
			return fmt.Errorf("%s holds the token of %q and other users can read it: run chmod 600 %s, or write the token as ${VAR}", path, server.Name, path)
		}
	}
	return nil
}

// loadSecrets reads fleet.env beside the file, for the ${VAR}s the file refers to that the
// environment does not set.
func loadSecrets(path string, raw []byte) error {
	referenced, err := config.RefVars(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	envFile := filepath.Join(filepath.Dir(path), EnvFileName)
	info, err := config.CheckEnvFile(envFile)
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	_, err = config.LoadEnvFile(envFile, referenced)
	return err
}

func (c *Config) validate() error {
	if len(c.Servers) == 0 {
		return errors.New("the file lists no servers, put them under the key servers")
	}
	seen := map[string]bool{}
	for i, server := range c.Servers {
		if err := server.validate(); err != nil {
			return fmt.Errorf("servers[%d] %q: %w", i, server.Name, err)
		}
		if seen[server.Name] {
			return fmt.Errorf("servers[%d]: the name %q is used twice", i, server.Name)
		}
		seen[server.Name] = true
	}
	return nil
}

func (s Server) validate() error {
	if !serverName.MatchString(s.Name) {
		return errors.New("name must start with a letter or digit and hold only letters, digits, . _ -")
	}
	if err := s.validateURL(); err != nil {
		return err
	}
	if s.Token == "" {
		return errors.New("token is empty (an unset ${VAR} is refused, an empty one too)")
	}
	return s.validateTLSCA()
}

func (s Server) validateURL() error {
	address, err := url.Parse(s.URL)
	switch {
	case err != nil || address.Host == "":
		return errors.New("url must be an address such as https://host:9311")
	case address.Scheme != "http" && address.Scheme != "https":
		return errors.New("url must start with http:// or https://")
	case address.User != nil:
		return errors.New("url must not carry a user or password: the token is its own field")
	case address.RawQuery != "" || address.Fragment != "":
		return errors.New("url must not have a query or fragment")
	}
	return nil
}

func (s Server) validateTLSCA() error {
	if s.TLSCA == "" {
		return nil
	}
	if !filepath.IsAbs(s.TLSCA) {
		return errors.New("tls_ca must be an absolute path")
	}
	if !strings.HasPrefix(s.URL, "https://") {
		return errors.New("tls_ca only means something for an https:// url")
	}
	return nil
}
