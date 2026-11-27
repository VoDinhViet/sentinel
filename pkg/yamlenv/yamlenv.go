// Package yamlenv reads YAML files that keep their secrets out of the file: a value may be written
// ${VAR}, and the variable comes from the environment or from an env file beside the config. It is
// shared by the agent config, the fleet file and the control plane config.
package yamlenv

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"

	"go.yaml.in/yaml/v3"
)

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand replaces ${VAR} references in every scalar value of the document (not in comments or
// keys) with the value lookup gives. A variable lookup does not know is added to missing, so the
// caller can report all of them at once. Expanding after parsing means a secret that holds YAML
// syntax cannot change the structure.
func Expand(node *yaml.Node, lookup func(name string) (string, bool), missing map[string]bool) {
	if node.Kind == yaml.ScalarNode {
		node.Value = envRef.ReplaceAllStringFunc(node.Value, func(ref string) string {
			name := envRef.FindStringSubmatch(ref)[1]
			value, ok := lookup(name)
			if !ok {
				missing[name] = true
			}
			return value
		})
		return
	}
	for i, child := range node.Content {
		if node.Kind == yaml.MappingNode && i%2 == 0 {
			continue // mapping key
		}
		Expand(child, lookup, missing)
	}
}

// eachRef calls visit for every ${VAR} in the values of a YAML document (not in comments or keys).
func eachRef(node *yaml.Node, visit func(name string)) {
	if node.Kind == yaml.ScalarNode {
		for _, match := range envRef.FindAllStringSubmatch(node.Value, -1) {
			visit(match[1])
		}
		return
	}
	for i, child := range node.Content {
		if node.Kind == yaml.MappingNode && i%2 == 0 {
			continue
		}
		eachRef(child, visit)
	}
}

// Refs lists every variable the document refers to with ${VAR}, set or not, sorted.
func Refs(raw []byte) ([]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	names := map[string]bool{}
	eachRef(&doc, func(name string) { names[name] = true })
	return slices.Sorted(maps.Keys(names)), nil
}

// DecodeStrict decodes YAML into target, optionally expanding ${VAR} in values from the
// environment, and rejects unknown fields.
func DecodeStrict(raw []byte, target any, expandVars bool) error {
	if expandVars {
		return DecodeStrictWith(raw, target, os.LookupEnv)
	}
	return DecodeStrictWith(raw, target, nil)
}

// DecodeStrictWith is DecodeStrict that expands ${VAR} from lookup instead of the environment, and
// not at all when lookup is nil.
func DecodeStrictWith(raw []byte, target any, lookup func(name string) (string, bool)) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil
	}
	if lookup != nil {
		missing := map[string]bool{}
		Expand(&doc, lookup, missing)
		if len(missing) > 0 {
			return fmt.Errorf("unset environment variables: %v", slices.Sorted(maps.Keys(missing)))
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

// MappingValue is the value of a key of a YAML mapping (or of the mapping at the top of a
// document), nil when there is none.
func MappingValue(node *yaml.Node, key string) *yaml.Node {
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
