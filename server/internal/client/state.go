package client

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"sentinel/pkg/pki"
	"sentinel/pkg/yamlenv"
)

// Files kept in the directory of sentinelctl. ctl.yaml has nothing secret (the address and the
// fingerprint of the CA); session is the token, mode 0600.
const (
	configFile  = "ctl.yaml"
	sessionFile = "session"
)

// ErrNoSession means nobody is signed in on this machine.
var ErrNoSession = errors.New("not signed in: run sentinelctl login")

// Config is where the control plane is and which CA to trust. The CA is given by its fingerprint, the
// one `sentinel-server init` printed, so there is no file to copy: the server shows its CA and it is
// checked against the fingerprint on every connection.
type Config struct {
	URL           string `yaml:"url"`
	CAFingerprint string `yaml:"ca_fingerprint"`
}

// DefaultDir is ~/.config/sentinel (or the platform's equivalent).
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the config directory: %w (use --dir)", err)
	}
	return filepath.Join(base, "sentinel"), nil
}

// Validate checks the address and the fingerprint, and returns the host to check the certificate for.
func (c Config) Validate() (host string, err error) {
	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("url %q must look like https://host:9321", c.URL)
	}
	if _, err := pki.ParseFingerprint(c.CAFingerprint); err != nil {
		return "", fmt.Errorf("ca_fingerprint: %w", err)
	}
	return parsed.Hostname(), nil
}

// LoadConfig reads ctl.yaml of dir.
func LoadConfig(dir string) (Config, error) {
	raw, err := os.ReadFile(filepath.Join(dir, configFile))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("no %s: run sentinelctl login --url … --ca-fingerprint …", filepath.Join(dir, configFile))
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yamlenv.DecodeStrict(raw, &cfg, false); err != nil {
		return Config{}, fmt.Errorf("%s: %w", filepath.Join(dir, configFile), err)
	}
	if _, err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", filepath.Join(dir, configFile), err)
	}
	return cfg, nil
}

// SaveConfig writes ctl.yaml, making dir (0700) if it is not there.
func SaveConfig(dir string, cfg Config) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	text := fmt.Sprintf("# sentinelctl: where the control plane is, and the CA it must have. No secret here.\nurl: %q\nca_fingerprint: %q\n", cfg.URL, cfg.CAFingerprint)
	return pki.WritePublic(filepath.Join(dir, configFile), []byte(text))
}

// LoadSession reads the token of the session. A file that others can read is refused: the token is
// the sign-in.
func LoadSession(dir string) (string, error) {
	path := filepath.Join(dir, sessionFile)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoSession
	}
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s can be read by others (mode %o): run chmod 600 on it, or sentinelctl login again", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", ErrNoSession
	}
	return token, nil
}

// SaveSession writes the token, mode 0600.
func SaveSession(dir, token string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return pki.WritePrivate(filepath.Join(dir, sessionFile), []byte(token+"\n"))
}

// DeleteSession removes the token; no token is not an error.
func DeleteSession(dir string) error {
	if err := os.Remove(filepath.Join(dir, sessionFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
