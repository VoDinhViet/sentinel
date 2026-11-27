package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAppliesDefaultsAndAcceptsAGoodFile(t *testing.T) {
	cfg, err := Parse([]byte("hosts: [backup.example.com]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9320" || cfg.CtlListen != "127.0.0.1:9321" || cfg.StateDir != DefaultStateDir || cfg.OfflineAfter != 2*time.Minute {
		t.Errorf("defaults = %+v", cfg)
	}
	if cfg.JoinURL() != "https://backup.example.com:9320" || cfg.Port() != "9320" {
		t.Errorf("join url = %q, port %q", cfg.JoinURL(), cfg.Port())
	}
	cfg, err = Parse([]byte("hosts: ['10.0.0.5', '2001:db8::1']\nlisten: 0.0.0.0:443\noffline_after: 90s\n"))
	if err != nil || cfg.JoinURL() != "https://10.0.0.5:443" || cfg.OfflineAfter != 90*time.Second {
		t.Errorf("cfg = %+v, %v", cfg, err)
	}
}

func TestParseReportsEveryProblem(t *testing.T) {
	_, err := Parse([]byte("listen: 9320\nctl_listen: \"host:0\"\nstate_dir: relative/dir\nhosts: [\"bad host\", \"-x.com\"]\noffline_after: 5s\n"))
	if err == nil {
		t.Fatal("a bad file was accepted")
	}
	for _, want := range []string{"listen:", "ctl_listen:", "state_dir:", `"bad host"`, `"-x.com"`, "offline_after:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %s: %v", want, err)
		}
	}
}

func TestParseRefusesAKeyItDoesNotKnownAndMissingHosts(t *testing.T) {
	if _, err := Parse([]byte("hosts: [a.example.com]\nlistn: \":1\"\n")); err == nil || !strings.Contains(err.Error(), "listn") {
		t.Errorf("a typo: err = %v", err)
	}
	if _, err := Parse([]byte("listen: \":9320\"\n")); err == nil || !strings.Contains(err.Error(), "hosts:") {
		t.Errorf("no hosts: err = %v", err)
	}
	if _, err := Parse([]byte("hosts: [a.example.com]\nlisten: \":9000\"\nctl_listen: \":9000\"\n")); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Errorf("same port twice: err = %v", err)
	}
}

func TestValidHostName(t *testing.T) {
	for host, want := range map[string]bool{
		"a": true, "backup.example.com": true, "web-01.example.com": true, "A.B": true, "xn--bcher-kva.example": true,
		"": false, ".": false, "a..b": false, "-a": false, "a-": false, "a.-b": false, "a b": false, "a_b": false,
		"a/b": false, strings.Repeat("a", 64): false, strings.Repeat("a.", 130): false,
	} {
		if got := validHostName(host); got != want {
			t.Errorf("validHostName(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestWriteThenLoadRoundTripsAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "server.yaml")
	cfg := Default()
	cfg.Hosts = []string{"backup.example.com", "10.0.0.5"}
	cfg.StateDir = t.TempDir()
	if err := Write(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("the written file does not load: %v", err)
	}
	if loaded.StateDir != cfg.StateDir || len(loaded.Hosts) != 2 || loaded.Hosts[1] != "10.0.0.5" || loaded.OfflineAfter != cfg.OfflineAfter {
		t.Errorf("loaded = %+v", loaded)
	}
	if err := Write(path, cfg); err == nil {
		t.Error("an existing config was overwritten")
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "#") {
		t.Error("the written file has no comments")
	}
}

func TestFilesAreInTheStateDirAndTheCACertificateIsLast(t *testing.T) {
	files := Config{StateDir: "/var/lib/x"}.Files()
	all := files.All()
	if all[len(all)-1] != "/var/lib/x/ca.pem" || files.DB() != "/var/lib/x/server.db" {
		t.Errorf("files = %v", all)
	}
	for _, path := range all {
		if filepath.Dir(path) != "/var/lib/x" {
			t.Errorf("%s is outside the state directory", path)
		}
	}
}

func TestTheExampleInDeployParses(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/server/server.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("deploy/server/server.example.yaml: %v", err)
	}
	if cfg.JoinURL() != "https://ctl.example.com:9320" {
		t.Errorf("JoinURL = %s", cfg.JoinURL())
	}
}
