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

// replaceFile keeps the previous content in path+".bak", then replaces path in one step. The new file
// has the mode and owner of the old one, and when path is a symlink the file behind it is replaced, not the link.
func replaceFile(path string, previous, next []byte) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := fileInfo.Mode().Perm()
	backup := path + ".bak"
	if err := os.WriteFile(backup, previous, mode); err != nil {
		return fmt.Errorf("write backup of the config: %w", err)
	}
	// WriteFile applies the mode only to a file it creates, and the backup holds every literal
	// password of the previous config, so one left by an earlier run must not stay wider.
	if err := os.Chmod(backup, mode); err != nil {
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
	keepOwner(tmp, fileInfo) // best effort: the daemon's user keeps reading the file
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir flushes a directory so a rename in it survives a crash. Best effort: the rename itself already happened.
func syncDir(dir string) {
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
}
