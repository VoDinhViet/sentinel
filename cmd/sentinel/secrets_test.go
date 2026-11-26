package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sentinel/internal/config"
	"sentinel/internal/restic"
)

const secretsConfig = "server_name: web-01\nstore: /tmp/sentinel-test.db\nrepositories:\n  local:\n    url: /tmp/sentinel-repo\n    password: \"${SECRET_TEST_PW}\"\njobs:\n  - name: documents\n    type: files\n    paths: [/tmp]\n    repositories: [local]\n    schedule: manual\n"

// terminalScript fakes a person at a terminal: replies are typed in order, saveAnswer is the reply
// to the save question, and existsFor decides what the repository check says per password.
type terminalScript struct {
	replies    []string
	prompts    []string
	saveAnswer bool
	asked      []string
	existsFor  func(password string) (bool, error)
}

func (s *terminalScript) terminal(interactive bool) terminalIO {
	return terminalIO{
		isTerminal: func() bool { return interactive },
		readSecret: func(prompt string) (string, error) {
			s.prompts = append(s.prompts, prompt)
			if len(s.replies) == 0 {
				return "", errors.New("no more replies")
			}
			reply := s.replies[0]
			s.replies = s.replies[1:]
			return reply, nil
		},
		confirm: func(question string) bool {
			s.asked = append(s.asked, question)
			return s.saveAnswer
		},
		repoExists: func(_ context.Context, cfg *config.Config, repoName string) (bool, error) {
			return s.existsFor(cfg.Repositories[repoName].Password)
		},
	}
}

func secretsApp(t *testing.T, script *terminalScript, interactive bool) (*cliApp, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(secretsConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECRET_TEST_PW", "")
	os.Unsetenv("SECRET_TEST_PW")
	t.Cleanup(func() { os.Unsetenv("SECRET_TEST_PW") })
	return &cliApp{configPath: path, term: script.terminal(interactive)}, filepath.Join(dir, config.EnvFileName)
}

func TestLoadConfigAsksForAMissingPasswordAndSavesIt(t *testing.T) {
	script := &terminalScript{replies: []string{"s3cret"}, saveAnswer: true, existsFor: func(string) (bool, error) { return true, nil }}
	cli, envFile := secretsApp(t, script, true)
	cfg, err := cli.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Repositories["local"].Password; got != "s3cret" {
		t.Errorf("password = %q", got)
	}
	if len(script.prompts) != 1 || !strings.Contains(script.prompts[0], "SECRET_TEST_PW") || !strings.Contains(script.prompts[0], "repository local") {
		t.Errorf("prompts = %q", script.prompts)
	}
	raw, err := os.ReadFile(envFile)
	if err != nil || string(raw) != "SECRET_TEST_PW=s3cret\n" {
		t.Errorf("env file = %q, %v", raw, err)
	}
	if info, err := os.Stat(envFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("env file mode = %v, %v", info, err)
	}
	// The next run reads it without asking.
	os.Unsetenv("SECRET_TEST_PW")
	again := &cliApp{configPath: cli.configPath, term: (&terminalScript{existsFor: script.existsFor}).terminal(true)}
	if cfg, err = again.loadConfig(); err != nil || cfg.Repositories["local"].Password != "s3cret" {
		t.Errorf("second load: %v, %v", cfg, err)
	}
}

func TestLoadConfigDoesNotSaveWhenDeclined(t *testing.T) {
	script := &terminalScript{replies: []string{"s3cret"}, existsFor: func(string) (bool, error) { return true, nil }}
	cli, envFile := secretsApp(t, script, true)
	if _, err := cli.loadConfig(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(envFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("env file must not exist: %v", err)
	}
	if len(script.asked) != 1 {
		t.Errorf("asked %v", script.asked)
	}
}

func TestLoadConfigAsksAgainOnAWrongPasswordThenGivesUp(t *testing.T) {
	wrong := &restic.ExitError{Code: 12}
	script := &terminalScript{replies: []string{"bad1", "good"}, existsFor: func(password string) (bool, error) {
		if password == "good" {
			return true, nil
		}
		return false, wrong
	}}
	cli, _ := secretsApp(t, script, true)
	cfg, err := cli.loadConfig()
	if err != nil || cfg.Repositories["local"].Password != "good" {
		t.Fatalf("cfg %v, err %v", cfg, err)
	}

	giveUp := &terminalScript{replies: []string{"a", "b", "c", "d"}, existsFor: func(string) (bool, error) { return false, wrong }}
	cli, _ = secretsApp(t, giveUp, true)
	if _, err := cli.loadConfig(); err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Errorf("err = %v, want wrong password after %d tries", err, maxPasswordTries)
	}
}

func TestLoadConfigNewRepositoryNeedsTheSamePasswordTwice(t *testing.T) {
	script := &terminalScript{replies: []string{"abc", "abd", "xyz", "xyz"}, existsFor: func(string) (bool, error) { return false, nil }}
	cli, _ := secretsApp(t, script, true)
	cfg, err := cli.loadConfig()
	if err != nil || cfg.Repositories["local"].Password != "xyz" {
		t.Fatalf("cfg %v, err %v (prompts %q)", cfg, err, script.prompts)
	}
}

func TestLoadConfigWithoutATerminalAndReadConfigNeverAsk(t *testing.T) {
	for name, load := range map[string]func(*cliApp) (*config.Config, error){
		"loadConfig, no terminal": (*cliApp).loadConfig,
		"readConfig, a terminal":  (*cliApp).readConfig,
	} {
		script := &terminalScript{replies: []string{"never"}, existsFor: func(string) (bool, error) { return true, nil }}
		cli, envFile := secretsApp(t, script, name == "readConfig, a terminal")
		_, err := load(cli)
		if err == nil || !strings.Contains(err.Error(), "SECRET_TEST_PW") || !strings.Contains(err.Error(), envFile) {
			t.Errorf("%s: err = %v, want the variable and the env file named", name, err)
		}
		if len(script.prompts) != 0 {
			t.Errorf("%s asked: %q", name, script.prompts)
		}
	}
}

func TestLoadConfigReadsTheEnvFileBesideTheConfig(t *testing.T) {
	script := &terminalScript{existsFor: func(string) (bool, error) { return true, nil }}
	cli, envFile := secretsApp(t, script, false)
	if err := os.WriteFile(envFile, []byte("SECRET_TEST_PW=fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := cli.readConfig()
	if err != nil || cfg.Repositories["local"].Password != "fromfile" {
		t.Fatalf("cfg %v, err %v", cfg, err)
	}
	if !slices.Equal(script.prompts, nil) {
		t.Errorf("prompts = %q", script.prompts)
	}
}

func TestBareSentinelPrintsHelpWithoutATerminal(t *testing.T) {
	cli := &cliApp{term: (&terminalScript{}).terminal(false)}
	root := newRootCmd()
	var output strings.Builder
	root.SetOut(&output)
	root.SetArgs([]string{})
	// newRootCmd builds its own app, so drive the same entry point directly.
	if err := cli.runDefault(root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Các lệnh:") {
		t.Errorf("want the help, got %q", output.String())
	}
}

func TestLoadConfigOldFormatFailsBeforeAsking(t *testing.T) {
	script := &terminalScript{replies: []string{"never"}, existsFor: func(string) (bool, error) { return true, nil }}
	cli, _ := secretsApp(t, script, true)
	old := strings.Replace(secretsConfig, "jobs:", "backups:", 1)
	if err := os.WriteFile(cli.configPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.loadConfig(); err == nil || !strings.Contains(err.Error(), "field backups not found") {
		t.Errorf("err = %v, want the unknown key", err)
	}
	if len(script.prompts) != 0 {
		t.Errorf("asked before the config was valid: %q", script.prompts)
	}
}

func TestUnverifiedPasswordIsTypedTwiceAndNeverOfferedForSaving(t *testing.T) {
	script := &terminalScript{replies: []string{"pw1", "pw1"}, saveAnswer: true, existsFor: func(string) (bool, error) { return false, errors.New("network down") }}
	cli, envFile := secretsApp(t, script, true)
	cfg, err := cli.loadConfig()
	if err != nil || cfg.Repositories["local"].Password != "pw1" {
		t.Fatalf("cfg %v, err %v", cfg, err)
	}
	if len(script.prompts) != 2 || !strings.Contains(script.prompts[1], "could not be checked") {
		t.Errorf("prompts = %q", script.prompts)
	}
	if _, err := os.Stat(envFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an unchecked password must not be saved: %v", err)
	}
	mismatch := &terminalScript{replies: []string{"a", "b", "c", "c"}, existsFor: func(string) (bool, error) { return false, errors.New("timeout") }}
	cli, _ = secretsApp(t, mismatch, true)
	if cfg, err = cli.loadConfig(); err != nil || cfg.Repositories["local"].Password != "c" {
		t.Errorf("a mistyped confirmation asks again: cfg %v, err %v", cfg, err)
	}
}

func TestEnvFileOthersCanWriteIsRefused(t *testing.T) {
	script := &terminalScript{existsFor: func(string) (bool, error) { return true, nil }}
	cli, envFile := secretsApp(t, script, false)
	if err := os.WriteFile(envFile, []byte("SECRET_TEST_PW=x\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(envFile, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.readConfig(); err == nil || !strings.Contains(err.Error(), "written by other users") {
		t.Errorf("err = %v", err)
	}
}

func TestEnvFileOnlySetsWhatTheConfigUses(t *testing.T) {
	script := &terminalScript{existsFor: func(string) (bool, error) { return true, nil }}
	cli, envFile := secretsApp(t, script, false)
	if err := os.WriteFile(envFile, []byte("SECRET_TEST_PW=x\nSENTINEL_TEST_UNRELATED=bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("SENTINEL_TEST_UNRELATED")
	if _, err := cli.readConfig(); err != nil {
		t.Fatal(err)
	}
	if v, set := os.LookupEnv("SENTINEL_TEST_UNRELATED"); set {
		t.Errorf("an unrelated variable was loaded: %q", v)
	}
}
