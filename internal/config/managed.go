package config

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"
)

// LocalOnlyKeys always come from the agent's own config file, never from the
// server: they identify the agent and say how to reach the server.
var LocalOnlyKeys = []string{"server_name", "store", "central", "restore_dir"}

func topMapping(raw []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(doc.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	m := doc.Content[0]
	if m.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config must be a YAML mapping")
	}
	return m, nil
}

// filterKeys returns the mapping's key/value pairs whose key satisfies keep.
func filterKeys(m *yaml.Node, keep func(string) bool) []*yaml.Node {
	var out []*yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if keep(m.Content[i].Value) {
			out = append(out, m.Content[i], m.Content[i+1])
		}
	}
	return out
}

// StripLocal removes the local-only keys, keeping comments and order.
func StripLocal(raw []byte) ([]byte, error) {
	m, err := topMapping(raw)
	if err != nil {
		return nil, err
	}
	m.Content = filterKeys(m, func(k string) bool { return !slices.Contains(LocalOnlyKeys, k) })
	if len(m.Content) == 0 {
		return []byte{}, nil
	}
	return yaml.Marshal(m)
}

// CheckManaged rejects managed configs that set local-only keys.
func CheckManaged(raw []byte) error {
	m, err := topMapping(raw)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := m.Content[i].Value; slices.Contains(LocalOnlyKeys, k) {
			return fmt.Errorf("%q can only be set in the agent's own config file", k)
		}
	}
	return nil
}

// MergeManaged combines the local-only keys of the agent's file with the
// managed config from the server.
func MergeManaged(local, managed []byte) ([]byte, error) {
	lm, err := topMapping(local)
	if err != nil {
		return nil, fmt.Errorf("local config: %w", err)
	}
	if err := CheckManaged(managed); err != nil {
		return nil, err
	}
	mm, err := topMapping(managed)
	if err != nil {
		return nil, fmt.Errorf("managed config: %w", err)
	}
	merged := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	merged.Content = append(filterKeys(lm, func(k string) bool { return slices.Contains(LocalOnlyKeys, k) }), mm.Content...)
	return yaml.Marshal(merged)
}
