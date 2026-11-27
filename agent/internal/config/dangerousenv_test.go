package config

import (
	"slices"
	"strings"
	"testing"
)

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
