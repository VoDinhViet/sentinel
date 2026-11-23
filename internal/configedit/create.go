package configedit

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sentinel/internal/config"
)

// Initial is what `sentinel init` asks for: enough to write a first working config.
type Initial struct {
	ServerName string
	Store      string // "" keeps the default (/var/lib/sentinel/state.db)
	Repository Repository
	Job        Job
	// Schedule and Retention become the defaults block, so the job itself stays short.
	Schedule  string
	Retention config.Retention
}

// ErrConfigExists means Create was asked to write over a config file that is already there.
var ErrConfigExists = errors.New("a config file already exists there")

const createdHeader = "# Sentinel configuration, written by `sentinel init`.\n" +
	"# ${VAR} is replaced by the environment variable VAR; secrets live there, never in this file.\n" +
	"# Schedules: \"daily 02:00\", \"weekly sun 03:00\", \"monthly 1 04:00\", \"hourly :15\", \"every 6h\", \"manual\" or cron.\n"

// Create writes a new config file at path. An existing file is refused unless force is set, in
// which case it is moved to path+".old". The file is built with the same edits the manage screen
// makes, so what init writes is exactly what the editor would write, and it is checked as it goes.
func Create(path string, initial Initial, force bool) error {
	if _, err := os.Stat(path); err == nil {
		if !force {
			return fmt.Errorf("%s: %w (use --force to replace it; the old one is kept as %s.old)", path, ErrConfigExists, path)
		}
		if err := os.Rename(path, path+".old"); err != nil {
			return fmt.Errorf("keep the old config: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create the config directory: %w", err)
	}
	header := createdHeader
	if name := strings.TrimSpace(initial.ServerName); name != "" {
		header += "server_name: " + name + "\n" // the header comment stays attached to this first key
	}
	if err := os.WriteFile(path, []byte(header), 0o640); err != nil {
		return fmt.Errorf("write the config: %w", err)
	}
	built := false
	defer func() {
		if !built {
			os.Remove(path)
		}
	}()
	if err := SaveRepository(path, "", initial.Repository); err != nil {
		return err
	}
	settings := Settings{ServerName: strings.TrimSpace(initial.ServerName), Store: strings.TrimSpace(initial.Store), DefaultSchedule: initial.Schedule, DefaultRetention: initial.Retention}
	if err := SaveSettings(path, settings); err != nil {
		return err
	}
	if err := Save(path, "", initial.Job); err != nil {
		return err
	}
	os.Remove(path + ".bak") // the intermediate copies of a file that did not exist yet
	built = true
	return nil
}

// EnsureSecret makes sure the env file defines the variable, adding a random value when it does
// not (mode 0600). It returns whether it wrote one. An existing value is never touched.
func EnsureSecret(envFile, name string) (bool, error) {
	if !envName.MatchString(name) {
		return false, fmt.Errorf("%q is not an environment variable name", name)
	}
	raw, err := os.ReadFile(envFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), name+"=") {
			return false, nil
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(envFile), 0o750); err != nil {
		return false, err
	}
	file, err := os.OpenFile(envFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	prefix := ""
	if len(raw) > 0 && !strings.HasSuffix(string(raw), "\n") {
		prefix = "\n"
	}
	_, writeErr := fmt.Fprintf(file, "%s%s=%s\n", prefix, name, base64.RawURLEncoding.EncodeToString(secret))
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr == nil, writeErr
}
