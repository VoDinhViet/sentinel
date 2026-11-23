package config

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// EnvFileName is the file beside the config that holds the secrets its ${VAR} references name.
const EnvFileName = "sentinel.env"

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// LoadEnvFile sets the variables of an env file (KEY=value lines, # comments, an optional
// "export " prefix, values bare or in '…' or "…") that the environment does not define yet, so
// the environment always wins. A missing file is not an error. It returns the names it set.
func LoadEnvFile(path string) ([]string, error) {
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
		if _, set := os.LookupEnv(name); set {
			continue
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
