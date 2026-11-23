package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func touch(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveConfigPathOrder(t *testing.T) {
	dir := t.TempDir()
	systemFile := touch(t, filepath.Join(dir, "etc", "config.yaml"))
	userFile := touch(t, filepath.Join(dir, "home", "config.yaml"))
	missing := filepath.Join(dir, "missing.yaml")

	for name, tc := range map[string]struct {
		locations configLocations
		want      string
	}{
		"flag wins over everything":          {configLocations{flagPath: "/flag.yaml", envPath: "/env.yaml", system: systemFile, user: userFile}, "/flag.yaml"},
		"env wins over the default files":    {configLocations{envPath: "/env.yaml", system: systemFile, user: userFile}, "/env.yaml"},
		"system file before the user file":   {configLocations{system: systemFile, user: userFile}, systemFile},
		"user file when there is no system":  {configLocations{system: missing, user: userFile}, userFile},
		"user file when the system is empty": {configLocations{user: userFile}, userFile},
		"an explicit missing path is kept":   {configLocations{flagPath: missing, system: systemFile}, missing},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.locations.resolve()
			if err != nil || got != tc.want {
				t.Errorf("resolve() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestResolveConfigPathExplainsWhatToDoWhenNothingExists(t *testing.T) {
	dir := t.TempDir()
	_, err := configLocations{system: filepath.Join(dir, "a.yaml"), user: filepath.Join(dir, "b.yaml")}.resolve()
	if err == nil {
		t.Fatal("want an error when no file exists")
	}
	for _, want := range []string{"a.yaml", "b.yaml", "config.user.example.yaml", "--config", "SENTINEL_CONFIG"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestResolveConfigPathIgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	if _, err := (configLocations{system: dir}).resolve(); err == nil {
		t.Error("a directory is not a config file")
	}
}

func TestCurrentConfigLocationsReadTheEnvironment(t *testing.T) {
	t.Setenv("SENTINEL_CONFIG", "/from/env.yaml")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	locations := currentConfigLocations("")
	if locations.envPath != "/from/env.yaml" || locations.user != "/xdg/sentinel/config.yaml" || locations.system != systemConfigPath {
		t.Errorf("locations = %+v", locations)
	}
	if got := currentConfigLocations("/flag.yaml"); got.flagPath != "/flag.yaml" {
		t.Errorf("flag path = %q", got.flagPath)
	}
}
