package restic

import (
	"strings"
	"testing"

	"sentinel/internal/config"
)

// envValues groups an environment list by variable name, in order.
func envValues(env []string) map[string][]string {
	valuesByName := map[string][]string{}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		valuesByName[name] = append(valuesByName[name], value)
	}
	return valuesByName
}

// restic starts the dump tools, rclone and its own helpers with the environment it was given, so what
// Sentinel inherits (a notifier token, the password of another repository) must not be passed along
// when the config only needs it somewhere else.
func TestEnvDropsInheritedVariablesAndKeepsWhatTheConfigGives(t *testing.T) {
	t.Setenv("TG_TOKEN", "bot-secret")
	t.Setenv("OTHER_REPO_PW", "other-password")
	t.Setenv("AWS_ACCESS_KEY_ID", "inherited-key")
	t.Setenv("AWS_REGION", "eu-west-1") // not referenced by the config: an ambient setting restic may use
	client := &Client{
		Repo: config.Repository{
			URL: "s3:host/bucket", Password: "repo-password",
			Env: map[string]string{"AWS_ACCESS_KEY_ID": "configured-key"},
		},
		ExtraEnv: []string{"PGPASSWORD=db-password"},
		DropEnv:  []string{"TG_TOKEN", "OTHER_REPO_PW", "AWS_ACCESS_KEY_ID"},
	}
	valuesByName := envValues(client.env())

	for _, gone := range []string{"TG_TOKEN", "OTHER_REPO_PW"} {
		if values, ok := valuesByName[gone]; ok {
			t.Errorf("%s was passed to restic: %v", gone, values)
		}
	}
	want := map[string]string{
		"RESTIC_REPOSITORY": "s3:host/bucket",
		"RESTIC_PASSWORD":   "repo-password",
		"AWS_ACCESS_KEY_ID": "configured-key", // the inherited value is dropped, the configured one is the only one
		"PGPASSWORD":        "db-password",
		"AWS_REGION":        "eu-west-1",
	}
	for name, value := range want {
		if values := valuesByName[name]; len(values) != 1 || values[0] != value {
			t.Errorf("%s = %v, want exactly [%s]", name, values, value)
		}
	}
}

// A config may use ${HOME} in a path; restic still needs HOME for its cache.
func TestEnvKeepsProcessSettingsEvenWhenTheConfigRefersToThem(t *testing.T) {
	t.Setenv("HOME", "/root")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("XDG_CACHE_HOME", "/var/cache")
	client := &Client{Repo: config.Repository{URL: "/repo", Password: "pw"}, DropEnv: []string{"HOME", "LC_ALL", "XDG_CACHE_HOME"}}
	valuesByName := envValues(client.env())
	for name, want := range map[string]string{"HOME": "/root", "LC_ALL": "C.UTF-8", "XDG_CACHE_HOME": "/var/cache"} {
		if values := valuesByName[name]; len(values) != 1 || values[0] != want {
			t.Errorf("%s = %v, want [%s]", name, values, want)
		}
	}
}

func TestEnvWithoutDropEnvInheritsEverything(t *testing.T) {
	t.Setenv("SOME_SETTING", "x")
	client := &Client{Repo: config.Repository{URL: "/repo", Password: "pw"}}
	if values := envValues(client.env())["SOME_SETTING"]; len(values) != 1 || values[0] != "x" {
		t.Errorf("SOME_SETTING = %v, want it inherited", values)
	}
}
