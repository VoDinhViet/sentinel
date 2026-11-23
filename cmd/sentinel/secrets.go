package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/configedit"
	"sentinel/internal/restic"
)

// maxPasswordTries is how often a wrong repository password is asked for again.
const maxPasswordTries = 3

// terminalIO is what loading the config needs from a person at a terminal. They are fields so
// tests can replace them.
type terminalIO struct {
	isTerminal func() bool
	// readSecret shows the prompt and reads one line without echoing it.
	readSecret func(prompt string) (string, error)
	confirm    func(question string) bool
	// repoExists reports whether the repository is already initialized with the password in cfg.
	repoExists func(ctx context.Context, cfg *config.Config, repoName string) (bool, error)
}

func defaultTerminal() terminalIO {
	return terminalIO{
		isTerminal: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
		},
		readSecret: func(prompt string) (string, error) {
			fmt.Fprint(os.Stderr, prompt)
			secret, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(secret), err
		},
		confirm: func(question string) bool {
			fmt.Fprint(os.Stderr, question+" [y/N] ")
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			return strings.EqualFold(strings.TrimSpace(answer), "y")
		},
		repoExists: func(ctx context.Context, cfg *config.Config, repoName string) (bool, error) {
			client, err := (&backup.Runner{Config: cfg}).Restic(repoName)
			if err != nil {
				return false, err
			}
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			return client.Exists(ctx)
		},
	}
}

// loadConfig reads the config; on a terminal it asks for the ${VAR}s that are not set yet.
func (a *app) loadConfig() (*config.Config, error) { return a.load(true) }

// readConfig reads the config and never asks (the daemon, scripts).
func (a *app) readConfig() (*config.Config, error) { return a.load(false) }

func (a *app) load(mayAsk bool) (*config.Config, error) {
	if a.configErr != nil {
		return nil, a.configErr
	}
	envFile := filepath.Join(filepath.Dir(a.configPath), config.EnvFileName)
	warnIfOpen(envFile)
	if _, err := config.LoadEnvFile(envFile); err != nil {
		return nil, fmt.Errorf("read %s: %w", envFile, err)
	}
	raw, err := os.ReadFile(a.configPath)
	if err != nil {
		return nil, err
	}
	missing, err := config.MissingVars(raw)
	if err != nil {
		return nil, err
	}
	var asked map[string]string
	if len(missing) > 0 {
		if !mayAsk || !a.term.isTerminal() {
			return nil, fmt.Errorf("unset environment variables: %v (set them in the environment or in %s)", missing, envFile)
		}
		if asked, err = a.askMissing(raw, missing); err != nil {
			return nil, err
		}
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return nil, err
	}
	if len(asked) > 0 {
		if cfg, err = a.verifyAsked(raw, cfg, asked); err != nil {
			return nil, err
		}
		a.offerSave(envFile, asked)
	}
	return cfg, nil
}

// askMissing reads each unset variable from the terminal and sets it for this process only.
func (a *app) askMissing(raw []byte, missing []string) (map[string]string, error) {
	repoByVar := map[string]string{}
	passwordVars, err := config.PasswordVars(raw)
	if err != nil {
		return nil, err
	}
	for repoName, name := range passwordVars {
		repoByVar[name] = repoName
	}
	asked := map[string]string{}
	for _, name := range missing {
		prompt := name + ": "
		if repoName, ok := repoByVar[name]; ok {
			prompt = fmt.Sprintf("%s (password of repository %s): ", name, repoName)
		}
		value, err := a.term.readSecret(prompt)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if value == "" {
			return nil, fmt.Errorf("%s is empty", name)
		}
		if err := os.Setenv(name, value); err != nil {
			return nil, err
		}
		asked[name] = value
	}
	return asked, nil
}

// verifyAsked checks each repository password that was just typed: a wrong one is asked for
// again, and a repository that does not exist yet needs the password typed twice, because it
// becomes the repository's password at `sentinel backup init`.
func (a *app) verifyAsked(raw []byte, cfg *config.Config, asked map[string]string) (*config.Config, error) {
	passwordVars, err := config.PasswordVars(raw)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	for _, repoName := range slices.Sorted(maps.Keys(passwordVars)) {
		name := passwordVars[repoName]
		if _, wasAsked := asked[name]; !wasAsked {
			continue
		}
		for try := 1; ; try++ {
			exists, err := a.term.repoExists(ctx, cfg, repoName)
			problem := ""
			switch {
			case err == nil && exists:
			case err == nil:
				again, readErr := a.term.readSecret(fmt.Sprintf("Repository %s is new: type the password of %s again: ", repoName, name))
				if readErr != nil {
					return nil, readErr
				}
				if again != os.Getenv(name) {
					problem = "the two passwords differ"
				}
			case restic.IsWrongPass(err):
				problem = "wrong password for repository " + repoName
			default:
				slog.Warn("could not check the repository password", "repository", repoName, "err", err)
			}
			if problem == "" {
				break
			}
			if try >= maxPasswordTries {
				return nil, errors.New(problem)
			}
			fmt.Fprintln(os.Stderr, problem+", try again")
			value, err := a.term.readSecret(name + ": ")
			if err != nil {
				return nil, err
			}
			if value == "" {
				return nil, fmt.Errorf("%s is empty", name)
			}
			if err := os.Setenv(name, value); err != nil {
				return nil, err
			}
			asked[name] = value
			if cfg, err = config.Parse(raw); err != nil {
				return nil, err
			}
		}
	}
	return cfg, nil
}

// offerSave keeps the typed values in the env file beside the config when the user agrees.
func (a *app) offerSave(envFile string, asked map[string]string) {
	if !a.term.confirm(fmt.Sprintf("Save to %s (readable only by you)?", envFile)) {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(asked)) {
		if err := configedit.SaveSecret(envFile, name, os.Getenv(name)); err != nil {
			fmt.Fprintf(os.Stderr, "could not save %s: %v\n", name, err)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "Saved. Keep a copy of the repository password somewhere else: losing it means losing every backup.\n")
}

// warnIfOpen warns when other users can read the env file; it never prints a value.
func warnIfOpen(envFile string) {
	if fileInfo, err := os.Stat(envFile); err == nil && fileInfo.Mode().Perm()&0o077 != 0 {
		slog.Warn("the env file is readable by other users", "file", envFile, "fix", "chmod 600 "+envFile)
	}
}
