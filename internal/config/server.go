package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ServerConfig configures `sentinel server run`.
type ServerConfig struct {
	Listen string `yaml:"listen"`
	TLS    *struct {
		Cert string `yaml:"cert"`
		Key  string `yaml:"key"`
	} `yaml:"tls"`
	DB string `yaml:"db"`
	// PublicURL is how agents reach the server; shown in install instructions.
	PublicURL string `yaml:"public_url"`
	Admin     struct {
		Username     string `yaml:"username"`
		PasswordHash string `yaml:"password_hash"`
	} `yaml:"admin"`
	// OfflineAfter marks an agent offline when it has not reported for this long.
	OfflineAfter time.Duration `yaml:"offline_after"`
	// SecureCookies sets the Secure flag; on by default, disable only for
	// plain-HTTP testing.
	SecureCookies *bool         `yaml:"secure_cookies"`
	Notifiers     Notifiers     `yaml:"notifiers"`
	Alert         AlertSettings `yaml:"alert"`
}

func LoadServer(path string) (*ServerConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseServer(raw)
}

func ParseServer(raw []byte) (*ServerConfig, error) {
	var c ServerConfig
	if err := decodeStrict(raw, &c, true); err != nil {
		return nil, err
	}
	if c.Listen == "" {
		c.Listen = ":8443"
	}
	if c.DB == "" {
		c.DB = "/var/lib/sentinel/server.db"
	}
	if c.OfflineAfter == 0 {
		c.OfflineAfter = 3 * time.Minute
	}
	if c.Alert.Cooldown == 0 {
		c.Alert.Cooldown = time.Hour
	}
	if c.SecureCookies == nil {
		on := true
		c.SecureCookies = &on
	}

	var errs []error
	if c.Admin.Username == "" || c.Admin.PasswordHash == "" {
		errs = append(errs, errors.New("admin.username and admin.password_hash are required (create a hash with `sentinel server hash-password`)"))
	} else if !strings.HasPrefix(c.Admin.PasswordHash, "$2") {
		errs = append(errs, errors.New("admin.password_hash must be a bcrypt hash"))
	}
	if c.TLS != nil && (c.TLS.Cert == "" || c.TLS.Key == "") {
		errs = append(errs, errors.New("tls.cert and tls.key are both required"))
	}
	if c.PublicURL != "" && !strings.HasPrefix(c.PublicURL, "http") {
		errs = append(errs, fmt.Errorf("public_url %q must start with http:// or https://", c.PublicURL))
	}
	return &c, errors.Join(errs...)
}
