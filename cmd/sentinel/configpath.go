package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// systemConfigPath is where a server install keeps its config.
const systemConfigPath = "/etc/sentinel/config.yaml"

// configLocations says where the config is looked for when --config is not given.
type configLocations struct {
	flagPath string // --config
	envPath  string // $SENTINEL_CONFIG
	system   string // the server install path
	user     string // the per-user path, "" when the user's config directory is unknown
}

func currentConfigLocations(flagPath string) configLocations {
	locations := configLocations{flagPath: flagPath, envPath: os.Getenv("SENTINEL_CONFIG"), system: systemConfigPath}
	if dir, err := os.UserConfigDir(); err == nil {
		locations.user = filepath.Join(dir, "sentinel", "config.yaml")
	}
	return locations
}

// resolve picks the config file: --config, then $SENTINEL_CONFIG, then the first
// of the system and per-user paths that exists. A path given explicitly is
// returned as it is, so a typo fails when the file is read instead of falling
// back to another file silently.
func (l configLocations) resolve() (string, error) {
	if l.flagPath != "" {
		return l.flagPath, nil
	}
	if l.envPath != "" {
		return l.envPath, nil
	}
	var tried []string
	for _, candidate := range []string{l.system, l.user} {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
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
