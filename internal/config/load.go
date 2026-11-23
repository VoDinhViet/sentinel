// Package config loads and validates the sentinel YAML configuration.
package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"regexp"
	"slices"
	"sync"

	"go.yaml.in/yaml/v3"
)

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv replaces ${VAR} references in every scalar value of the document
// (not in comments or keys). Unset variables are an error so a missing secret
// is caught at startup instead of producing an empty password. Expanding after
// parsing means a secret containing YAML syntax cannot change the structure.
func expandEnv(node *yaml.Node, missingVars map[string]bool) {
	if node.Kind == yaml.ScalarNode {
		node.Value = envRef.ReplaceAllStringFunc(node.Value, func(ref string) string {
			name := envRef.FindStringSubmatch(ref)[1]
			value, ok := os.LookupEnv(name)
			if !ok {
				missingVars[name] = true
			}
			return value
		})
		return
	}
	for i, child := range node.Content {
		if node.Kind == yaml.MappingNode && i%2 == 0 {
			continue // mapping key
		}
		expandEnv(child, missingVars)
	}
}

// MissingVars lists the ${VAR} references of a config document whose variable is not set.
func MissingVars(raw []byte) ([]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// A config in an old format fails here, before anybody is asked for a value it may not need.
	if len(doc.Content) > 0 {
		if err := checkRenamedKeys(doc.Content[0]); err != nil {
			return nil, err
		}
	}
	missingVars := map[string]bool{}
	expandEnv(&doc, missingVars)
	return slices.Sorted(maps.Keys(missingVars)), nil
}

// RefVars lists every variable the config document refers to with ${VAR}, set or not.
func RefVars(raw []byte) ([]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	names := map[string]bool{}
	var walk func(node *yaml.Node)
	walk = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode {
			for _, match := range envRef.FindAllStringSubmatch(node.Value, -1) {
				names[match[1]] = true
			}
			return
		}
		for i, child := range node.Content {
			if node.Kind == yaml.MappingNode && i%2 == 0 {
				continue
			}
			walk(child)
		}
	}
	walk(&doc)
	return slices.Sorted(maps.Keys(names)), nil
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
	if err := decodeStrict(raw, &cfg, opts.ExpandEnv); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Monitor != nil {
		monitorWarned.Do(func() {
			slog.Warn("config: the monitor: section is ignored; Sentinel no longer monitors server resources")
		})
	}
	return &cfg, nil
}

var monitorWarned sync.Once

// decodeStrict decodes YAML into target, optionally expanding ${VAR} in values,
// and rejects unknown fields.
func decodeStrict(raw []byte, target any, expandVars bool) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil
	}
	if err := checkRenamedKeys(doc.Content[0]); err != nil {
		return err
	}
	if expandVars {
		missingVars := map[string]bool{}
		expandEnv(&doc, missingVars)
		if len(missingVars) > 0 {
			return fmt.Errorf("unset environment variables: %v", slices.Sorted(maps.Keys(missingVars)))
		}
	}
	// Re-encode so the strict decoder can reject unknown fields.
	expanded, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(expanded))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	return nil
}

// renamedKeys maps top-level keys that were renamed to their new names, so an old config
// gets a pointer to the fix instead of "field not found".
var renamedKeys = map[string]string{
	"backups":             "jobs",
	"repo_check_schedule": "verify_schedule",
}

func checkRenamedKeys(root *yaml.Node) error {
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if newName, ok := renamedKeys[key.Value]; ok {
			return fmt.Errorf("parse config: line %d: %q was renamed to %q", key.Line, key.Value, newName)
		}
	}
	return nil
}

// LoadNotifier reads the one notifier of that kind (telegram, smtp, discord or slack) from the
// notifiers: section of a config file, with ${VAR} expanded. Only that notifier's own variables
// need to be set, so it can be tried while the rest of the file's secrets are not. The result
// has no notifier at all when the file has none of that kind.
func LoadNotifier(path, kind string) (Notifiers, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Notifiers{}, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return Notifiers{}, fmt.Errorf("parse config: %w", err)
	}
	section := mappingValue(&doc, "notifiers")
	if section == nil {
		return Notifiers{}, nil
	}
	notifier := mappingValue(section, kind)
	if notifier == nil {
		return Notifiers{}, nil
	}
	missingVars := map[string]bool{}
	expandEnv(notifier, missingVars)
	if len(missingVars) > 0 {
		return Notifiers{}, fmt.Errorf("unset environment variables: %v", slices.Sorted(maps.Keys(missingVars)))
	}
	only := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: kind}, notifier}}
	expanded, err := yaml.Marshal(only)
	if err != nil {
		return Notifiers{}, err
	}
	var notifiers Notifiers
	decoder := yaml.NewDecoder(bytes.NewReader(expanded))
	decoder.KnownFields(true)
	if err := decoder.Decode(&notifiers); err != nil {
		return Notifiers{}, fmt.Errorf("parse config: %w", err)
	}
	return notifiers, nil
}

// mappingValue is the value of a key of a YAML mapping (or of the mapping at the top of a
// document), nil when there is none.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
