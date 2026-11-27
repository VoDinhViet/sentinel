package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SystemPath is where a server install keeps its config.
const SystemPath = "/etc/sentinel/config.yaml"

// Locations says where the config is looked for when --config is not given.
type Locations struct {
	Flag   string // --config
	Env    string // $SENTINEL_CONFIG
	System string // the server install path
	User   string // the per-user path, "" when the user's config directory is unknown
}

// DefaultLocations reads the environment for the places the config may be.
func DefaultLocations(flagPath string) Locations {
	locations := Locations{Flag: flagPath, Env: os.Getenv("SENTINEL_CONFIG"), System: SystemPath}
	if dir, err := os.UserConfigDir(); err == nil {
		locations.User = filepath.Join(dir, "sentinel", "config.yaml")
	}
	return locations
}

// WritePath is where `sentinel init` writes: --config or $SENTINEL_CONFIG when given, the
// system path for root, the per-user path otherwise.
func (l Locations) WritePath(root bool) string {
	switch {
	case l.Flag != "":
		return l.Flag
	case l.Env != "":
		return l.Env
	case root || l.User == "":
		return l.System
	}
	return l.User
}

// Resolve picks the config file: --config, then $SENTINEL_CONFIG, then the first
// of the system and per-user paths that exists. A path given explicitly is
// returned as it is, so a typo fails when the file is read instead of falling
// back to another file silently.
func (l Locations) Resolve() (string, error) {
	if l.Flag != "" {
		return l.Flag, nil
	}
	if l.Env != "" {
		return l.Env, nil
	}
	var tried []string
	for _, candidate := range []string{l.System, l.User} {
		if candidate == "" {
			continue
		}
		if fileInfo, err := os.Stat(candidate); err == nil && !fileInfo.IsDir() {
			return candidate, nil
		}
		tried = append(tried, candidate)
	}
	return "", errors.New(missingConfigMessage(tried))
}

func missingConfigMessage(tried []string) string {
	return fmt.Sprintf("no config file found (looked in %s)\n"+
		"run `sentinel init` to create one by answering a form (or copy deploy/config.user.example.yaml / deploy/config.example.yaml),\n"+
		"or point to an existing one with --config or the SENTINEL_CONFIG environment variable",
		strings.Join(tried, ", "))
}
