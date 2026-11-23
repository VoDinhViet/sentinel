package configedit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// plainEnvValue is what an env file value may hold without quotes, so sh, systemd and docker
// all read it the same way.
var plainEnvValue = regexp.MustCompile(`^[A-Za-z0-9._~+/=:@%-]+$`)

// SaveSecret sets name=value in the env file: it replaces the line of that variable or adds one,
// keeps every other line, and leaves the file readable by its owner only (0600). The file is
// replaced in one step, so a crash cannot leave half of it.
func SaveSecret(envFile, name, value string) error {
	if !envNameRe.MatchString(name) {
		return fmt.Errorf("%q is not an environment variable name", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("a secret cannot contain a line break")
	}
	raw, err := os.ReadFile(envFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	line := name + "=" + quoteEnvValue(value)
	var lines []string
	replaced := false
	if len(raw) > 0 {
		for _, existing := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			bare := strings.TrimPrefix(strings.TrimSpace(existing), "export ")
			if strings.HasPrefix(strings.TrimSpace(bare), name+"=") {
				if !replaced {
					lines = append(lines, line)
				}
				replaced = true
				continue
			}
			lines = append(lines, existing)
		}
	}
	if !replaced {
		lines = append(lines, line)
	}
	if err := os.MkdirAll(filepath.Dir(envFile), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(envFile), ".sentinel-env-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), envFile)
}

func quoteEnvValue(value string) string {
	if plainEnvValue.MatchString(value) {
		return value
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(value)
	return `"` + escaped + `"`
}
