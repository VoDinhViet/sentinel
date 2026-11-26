package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Anyone who can create a folder where the admin browses chooses its name, and the name is drawn on the
// terminal. The list shows a cleaned name; the row keeps the real path so choosing it still works.
func TestFolderRowsShowCleanNamesAndKeepTheRealPath(t *testing.T) {
	root := t.TempDir()
	hostile := "uploads\x1b]52;c;ZXZpbA==\x07\x1b[2K"
	if err := os.Mkdir(filepath.Join(root, hostile), 0o755); err != nil {
		t.Skipf("this file system does not take control characters in names: %v", err)
	}
	rows := folderRows(root)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the one folder", rows)
	}
	if strings.ContainsAny(rows[0].name, "\x1b\x07") || rows[0].name != "uploads/" {
		t.Errorf("name = %q, want %q", rows[0].name, "uploads/")
	}
	if rows[0].path != filepath.Join(root, hostile) {
		t.Errorf("path = %q, want the real path so the choice still works", rows[0].path)
	}
}
