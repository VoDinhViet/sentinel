package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFolderRowsListsFoldersAndSymlinksToFoldersOnly(t *testing.T) {
	root := t.TempDir()
	for _, folder := range []string{"www", "data"} {
		if err := os.Mkdir(filepath.Join(root, folder), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"to-folder": filepath.Join(root, "www"),
		"to-file":   filepath.Join(root, "notes.txt"),
		"broken":    filepath.Join(root, "missing"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}

	var names []string
	for _, row := range folderRows(root) {
		names = append(names, row.name)
	}
	want := []string{"data/", "to-folder/", "www/"}
	if len(names) != len(want) {
		t.Fatalf("rows = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("rows = %v, want %v", names, want)
		}
	}
}
