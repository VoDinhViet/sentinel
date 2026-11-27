package config

import (
	"os"
	"path/filepath"

	"sentinel/pkg/yamlenv"
)

// EnvFileName is the file beside the config that holds the secrets its ${VAR} references name.
const EnvFileName = "sentinel.env"

// RefreshEnv loads from the env file beside the config the variables it refers to that are
// still unset, so a variable added while the manager is open (and a value put in the file since)
// is found by a test. It logs nothing, since a screen may be open, and does nothing if the file is
// not safe to read: the test then reports the variable as unset.
func RefreshEnv(configPath string) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return
	}
	referenced, err := yamlenv.Refs(raw)
	if err != nil {
		return
	}
	envFile := filepath.Join(filepath.Dir(configPath), EnvFileName)
	if info, err := yamlenv.CheckEnvFile(envFile); err == nil && info != nil {
		_, _ = yamlenv.LoadEnvFile(envFile, referenced)
	}
}
