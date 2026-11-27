// Package config loads and validates the sentinel YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"sentinel/pkg/notify"
	"sentinel/pkg/yamlenv"
)

// MissingVars lists the ${VAR} references of a config document whose variable is not set.
func MissingVars(raw []byte) ([]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// A config with a key Sentinel does not know fails here, before anybody is asked for a value it may not need.
	if err := rejectUnknownKeys(raw); err != nil {
		return nil, err
	}
	missingVars := map[string]bool{}
	yamlenv.Expand(&doc, os.LookupEnv, missingVars)
	return slices.Sorted(maps.Keys(missingVars)), nil
}

// rejectUnknownKeys fails when the document has a key Sentinel does not know. Other decoding problems
// are left for Parse: before the variables are set, a ${VAR} in a number or a duration does not decode.
func rejectUnknownKeys(raw []byte) error {
	err := yamlenv.DecodeStrict(raw, &Config{}, false)
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return err
	}
	var unknown []string
	for _, problem := range typeErr.Errors {
		if strings.Contains(problem, "not found in type") {
			unknown = append(unknown, problem)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("parse config: %s", strings.Join(unknown, "; "))
}

// wholeEnvRef matches a value that is exactly one ${VAR}.
var wholeEnvRef = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// PasswordVars maps each repository name to the variable its password is, for the repositories
// that write the password as one ${VAR}.
func PasswordVars(raw []byte) (map[string]string, error) {
	var doc struct {
		Repositories map[string]struct {
			Password string `yaml:"password"`
		} `yaml:"repositories"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	vars := map[string]string{}
	for name, repo := range doc.Repositories {
		if match := wholeEnvRef.FindStringSubmatch(repo.Password); match != nil {
			vars[name] = match[1]
		}
	}
	return vars, nil
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	return ParseWith(raw, ParseOptions{ExpandEnv: true})
}

type ParseOptions struct {
	// ExpandEnv replaces ${VAR} references. The config editor reads files with it
	// off, since secrets only exist in the environment of the running service.
	ExpandEnv bool
}

func ParseWith(raw []byte, opts ParseOptions) (*Config, error) {
	var cfg Config
	var err error
	if err = yamlenv.DecodeStrict(raw, &cfg, opts.ExpandEnv); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.EnvRefs, err = yamlenv.Refs(raw); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadNotifier reads the one notifier of that kind (telegram, smtp, discord or slack) from the
// notifiers: section of a config file, with ${VAR} expanded. Only that notifier's own variables
// need to be set, so it can be tried while the rest of the file's secrets are not. The result
// has no notifier at all when the file has none of that kind.
func LoadNotifier(path, kind string) (notify.Settings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return notify.Settings{}, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return notify.Settings{}, fmt.Errorf("parse config: %w", err)
	}
	section := yamlenv.MappingValue(&doc, "notifiers")
	if section == nil {
		return notify.Settings{}, nil
	}
	notifier := yamlenv.MappingValue(section, kind)
	if notifier == nil {
		return notify.Settings{}, nil
	}
	missingVars := map[string]bool{}
	yamlenv.Expand(notifier, os.LookupEnv, missingVars)
	if len(missingVars) > 0 {
		return notify.Settings{}, fmt.Errorf("unset environment variables: %v", slices.Sorted(maps.Keys(missingVars)))
	}
	only := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: kind}, notifier}}
	expanded, err := yaml.Marshal(only)
	if err != nil {
		return notify.Settings{}, err
	}
	var notifiers notify.Settings
	decoder := yaml.NewDecoder(bytes.NewReader(expanded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&notifiers); err != nil {
		return notify.Settings{}, fmt.Errorf("parse config: %w", err)
	}
	return notifiers, nil
}
