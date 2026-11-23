package config

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

func TestResolveOrder(t *testing.T) {
	dir := t.TempDir()
	systemFile := touch(t, filepath.Join(dir, "etc", "config.yaml"))
	userFile := touch(t, filepath.Join(dir, "home", "config.yaml"))
	missing := filepath.Join(dir, "missing.yaml")

	for name, tc := range map[string]struct {
		locations Locations
		want      string
	}{
		"flag wins over everything":          {Locations{Flag: "/flag.yaml", Env: "/env.yaml", System: systemFile, User: userFile}, "/flag.yaml"},
		"env wins over the default files":    {Locations{Env: "/env.yaml", System: systemFile, User: userFile}, "/env.yaml"},
		"system file before the user file":   {Locations{System: systemFile, User: userFile}, systemFile},
		"user file when there is no system":  {Locations{System: missing, User: userFile}, userFile},
		"user file when the system is empty": {Locations{User: userFile}, userFile},
		"an explicit missing path is kept":   {Locations{Flag: missing, System: systemFile}, missing},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.locations.Resolve()
			if err != nil || got != tc.want {
				t.Errorf("resolve() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestResolveExplainsWhatToDoWhenNothingExists(t *testing.T) {
	dir := t.TempDir()
	_, err := Locations{System: filepath.Join(dir, "a.yaml"), User: filepath.Join(dir, "b.yaml")}.Resolve()
	if err == nil {
		t.Fatal("want an error when no file exists")
	}
	for _, want := range []string{"a.yaml", "b.yaml", "sentinel init", "config.user.example.yaml", "--config", "SENTINEL_CONFIG"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestResolveIgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	if _, err := (Locations{System: dir}).Resolve(); err == nil {
		t.Error("a directory is not a config file")
	}
}

func TestDefaultLocationsReadTheEnvironment(t *testing.T) {
	t.Setenv("SENTINEL_CONFIG", "/from/env.yaml")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	locations := DefaultLocations("")
	if locations.Env != "/from/env.yaml" || locations.User != "/xdg/sentinel/config.yaml" || locations.System != SystemPath {
		t.Errorf("locations = %+v", locations)
	}
	if got := DefaultLocations("/flag.yaml"); got.Flag != "/flag.yaml" {
		t.Errorf("flag path = %q", got.Flag)
	}
}

func TestWritePathPicksWhereToWrite(t *testing.T) {
	locations := Locations{System: "/etc/sentinel/config.yaml", User: "/home/me/.config/sentinel/config.yaml"}
	for name, tc := range map[string]struct {
		locations Locations
		root      bool
		want      string
	}{
		"user":              {locations, false, "/home/me/.config/sentinel/config.yaml"},
		"root":              {locations, true, "/etc/sentinel/config.yaml"},
		"no user directory": {Locations{System: "/etc/sentinel/config.yaml"}, false, "/etc/sentinel/config.yaml"},
		"flag wins":         {Locations{Flag: "/x/c.yaml", Env: "/y.yaml", System: "/s", User: "/u"}, true, "/x/c.yaml"},
		"env beats default": {Locations{Env: "/y.yaml", System: "/s", User: "/u"}, false, "/y.yaml"},
	} {
		if got := tc.locations.WritePath(tc.root); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}
