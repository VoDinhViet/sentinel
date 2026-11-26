package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

// EnvFileName is the file beside the config that holds the secrets its ${VAR} references name.
const EnvFileName = "sentinel.env"

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// LoadEnvFile sets the variables of an env file (KEY=value lines, # comments, an optional
// "export " prefix, values bare or in '…' or "…") that the environment does not define yet, so
// the environment always wins. Only the names in wanted are read (the ${VAR}s the config
// refers to): other lines are skipped, so the file cannot set PATH or LD_PRELOAD for the
// tools Sentinel starts. A missing file is not an error. It returns the names it set.
func LoadEnvFile(path string, wanted []string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()

	var loaded []string
	scanner := bufio.NewScanner(file)
	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !found || !envName.MatchString(name) {
			return loaded, fmt.Errorf("%s:%d: want NAME=value", path, lineNum)
		}
		if _, set := os.LookupEnv(name); set || !slices.Contains(wanted, name) {
			continue
		}
		if IsDangerousEnvName(name) {
			continue // never from a file: it would reach restic and its tools (the config refuses it too)
		}
		parsed, err := parseEnvValue(strings.TrimSpace(value))
		if err != nil {
			return loaded, fmt.Errorf("%s:%d: %w", path, lineNum, err)
		}
		if err := os.Setenv(name, parsed); err != nil {
			return loaded, err
		}
		loaded = append(loaded, name)
	}
	return loaded, scanner.Err()
}

// parseEnvValue unquotes a value: '…' is literal, "…" understands \\ \" \$ and \`, a bare
// value is taken as it is.
func parseEnvValue(value string) (string, error) {
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1], nil
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		var out strings.Builder
		inner := value[1 : len(value)-1]
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) && strings.IndexByte("\\\"$`", inner[i+1]) >= 0 {
				i++
			}
			out.WriteByte(inner[i])
		}
		return out.String(), nil
	}
	if strings.HasPrefix(value, "'") || strings.HasPrefix(value, `"`) {
		return "", fmt.Errorf("unterminated quote")
	}
	return value, nil
}

// CheckEnvFile refuses an env file other users could have written or that belongs to someone
// else. It returns nil, nil for a file that does not exist.
func CheckEnvFile(envFile string) (os.FileInfo, error) {
	info, err := os.Stat(envFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", envFile, err)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s can be written by other users: run chmod 600 %s", envFile, envFile)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != 0 && int(stat.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("%s belongs to another user", envFile)
	}
	return info, nil
}

// RefreshEnv loads from the env file beside the config the variables it refers to that are
// still unset, so a variable added while the manager is open (and a value put in the file since)
// is found by a test. It logs nothing, since a screen may be open, and does nothing if the file is
// not safe to read: the test then reports the variable as unset.
func RefreshEnv(configPath string) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return
	}
	referenced, err := RefVars(raw)
	if err != nil {
		return
	}
	envFile := filepath.Join(filepath.Dir(configPath), EnvFileName)
	if info, err := CheckEnvFile(envFile); err == nil && info != nil {
		_, _ = LoadEnvFile(envFile, referenced)
	}
}
