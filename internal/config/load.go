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
