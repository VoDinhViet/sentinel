package configedit

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"sentinel/internal/config"
)

// editFile applies edit to the file's YAML, checks that the result is still a
// valid config, and replaces the file atomically with the same permissions.
func editFile(path string, edit func(root *yaml.Node, current *config.Config) error) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	current, err := config.ParseWith(raw, config.ParseOptions{ExpandEnv: false})
	if err != nil {
		return fmt.Errorf("config không đọc được nên không sửa: %w", err)
	}
	doc, root, err := parseDoc(raw)
	if err != nil {
		return err
	}
	if err := edit(root, current); err != nil {
		return err
	}
	edited, err := encodeDoc(doc)
	if err != nil {
		return err
	}
	if _, err := config.ParseWith(edited, config.ParseOptions{ExpandEnv: false}); err != nil {
		return invalid("thay đổi này làm config không hợp lệ: %v", err)
	}
	return replaceFile(path, raw, edited)
}

func replaceFile(path string, previous, next []byte) error {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := fileInfo.Mode().Perm()
	if err := os.WriteFile(path+".bak", previous, mode); err != nil {
		return fmt.Errorf("write backup of the config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sentinel-config-*")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(next); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}
