package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestIsDangerousEnvName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"PATH", true},
		{"path", true},
		{"LD_PRELOAD", true},
		{"LD_LIBRARY_PATH", true},
		{"DYLD_INSERT_LIBRARIES", true},
		{"BASH_ENV", true},
		{"IFS", true},
		{"RESTIC_PASSWORD_COMMAND", true},
		{"RESTIC_FROM_PASSWORD_COMMAND", true},
		{"AWS_ACCESS_KEY_ID", false},
		{"AWS_SECRET_ACCESS_KEY", false},
		{"RCLONE_CONFIG_S3_TYPE", false},
		{"RESTIC_PASSWORD", false},
		{"PGPASSWORD", false},
		{"TG_TOKEN", false},
		{"MYPATH", false},
		{"SSLKEYLOGFILE", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDangerousEnvName(tt.name); got != tt.want {
				t.Errorf("IsDangerousEnvName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// restic and the dump tools run as root, so a name that changes which program or library they load,
// or that makes restic run a command to get the password, must not come from the config.
func TestParseRejectsDangerousVariablesInRepositoryEnv(t *testing.T) {
	for _, name := range []string{"PATH", "LD_PRELOAD", "RESTIC_PASSWORD_COMMAND", "BASH_ENV"} {
		t.Run(name, func(t *testing.T) {
			raw := "repositories:\n  local:\n    url: /var/backups/repo\n    password: pw\n    env:\n      " + name + ": /tmp/evil\n"
			_, err := ParseWith([]byte(raw), ParseOptions{ExpandEnv: false})
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("err = %v, want a refusal naming %s", err, name)
			}
		})
	}
	raw := "repositories:\n  local:\n    url: /var/backups/repo\n    password: pw\n    env:\n      \"a b\": x\n"
	if _, err := ParseWith([]byte(raw), ParseOptions{ExpandEnv: false}); err == nil {
		t.Error("a name that is not a variable name must be refused")
	}
	ok := "repositories:\n  s3:\n    url: s3:host/bucket\n    password: pw\n    env:\n      AWS_ACCESS_KEY_ID: k\n"
	if _, err := ParseWith([]byte(ok), ParseOptions{ExpandEnv: false}); err != nil {
		t.Errorf("a normal variable must be accepted: %v", err)
	}
}

// A config may use ${ENV} or ${DEPLOY_COMMAND} for ordinary text, so referring to such a name is not an error:
// restic does not inherit what the config refers to (restic.Client.DropEnv), and the env file never sets the
// dangerous ones (see below).
func TestParseAcceptsReferencesToVariablesWithRiskyNames(t *testing.T) {
	for _, name := range []string{"ENV", "IFS", "DEPLOY_COMMAND", "PATH"} {
		t.Setenv(name, "value")
		raw := "server_name: \"${" + name + "}\"\nrepositories:\n  local:\n    url: /var/backups/repo\n    password: pw\n"
		if _, err := ParseWith([]byte(raw), ParseOptions{ExpandEnv: true}); err != nil {
			t.Errorf("${%s} must be accepted: %v", name, err)
		}
	}
}

func TestLoadEnvFileNeverSetsDangerousVariables(t *testing.T) {
	for _, name := range []string{"BASH_ENV", "RESTIC_PASSWORD_COMMAND", "LD_PRELOAD"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	t.Setenv("PW_FROM_FILE", "")
	os.Unsetenv("PW_FROM_FILE")
	path := filepath.Join(t.TempDir(), "sentinel.env")
	content := "BASH_ENV=/tmp/evil.sh\nRESTIC_PASSWORD_COMMAND=cat /etc/shadow\nLD_PRELOAD=/tmp/evil.so\nPW_FROM_FILE=ok\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	wanted := []string{"BASH_ENV", "RESTIC_PASSWORD_COMMAND", "LD_PRELOAD", "PW_FROM_FILE"}
	loaded, err := LoadEnvFile(path, wanted)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loaded, []string{"PW_FROM_FILE"}) {
		t.Errorf("loaded = %v, want only PW_FROM_FILE", loaded)
	}
	for _, name := range []string{"BASH_ENV", "RESTIC_PASSWORD_COMMAND", "LD_PRELOAD"} {
		if value, set := os.LookupEnv(name); set {
			t.Errorf("%s was set to %q from the env file", name, value)
		}
	}
	if os.Getenv("PW_FROM_FILE") != "ok" {
		t.Error("an ordinary variable must still be loaded")
	}
}

// The child processes of restic are given every value of the config explicitly, so they do not need
// the variables the config refers to; the config records them so those can be dropped from what they inherit.
func TestParseRecordsTheVariablesTheConfigRefersTo(t *testing.T) {
	t.Setenv("TG_TOKEN", "tok")
	t.Setenv("RESTIC_PW", "pw")
	cfg, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"RESTIC_PW", "TG_TOKEN"}; !slices.Equal(cfg.EnvRefs, want) {
		t.Errorf("EnvRefs = %v, want %v", cfg.EnvRefs, want)
	}
}
