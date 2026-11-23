package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareRestoreTargetCreatesAPrivateFolder(t *testing.T) {
	target := filepath.Join(t.TempDir(), "a", "b", "restore")
	if err := PrepareRestoreTarget(target); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Errorf("target = %v (%v), want a folder with mode 700", info, err)
	}
	if err := PrepareRestoreTarget(target); err != nil {
		t.Errorf("a folder this process made and owns must be accepted again: %v", err)
	}
}

// restic writes through whatever it finds at the target, as root, so a folder that someone else made
// first (and may have filled with symlinks) must be refused, not restored into.
func TestPrepareRestoreTargetRefusesAFolderOthersCouldPrepare(t *testing.T) {
	base := t.TempDir()
	makeDir := func(name string, mode os.FileMode) string {
		path := filepath.Join(base, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	realDir := makeDir("real", 0o700)
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{"empty", "", "absolute"},
		{"relative", "restore", "absolute"},
		{"relative with dots", "../restore", "absolute"},
		{"nul byte", base + "/x\x00y", "absolute"},
		{"symlink to a folder", link, "symbolic link"},
		{"a file", file, "not a folder"},
		{"writable by everyone", makeDir("open", 0o777), "can write"},
		{"writable by the group", makeDir("group", 0o770), "can write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := PrepareRestoreTarget(tt.target)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("PrepareRestoreTarget(%q) = %v, want an error containing %q", tt.target, err, tt.want)
			}
		})
	}
}

func TestPrepareRestoreTargetRefusesAFolderOfAnotherUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to give a folder to another user")
	}
	target := filepath.Join(t.TempDir(), "restore")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(target, 12345, 12345); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRestoreTarget(target); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Errorf("err = %v, want a refusal because another user owns the folder", err)
	}
}

func TestIsSystemRestoreTarget(t *testing.T) {
	tests := []struct {
		dir  string
		want bool
	}{
		{"/", true},
		{"/etc", true},
		{"/etc/", true},
		{"/etc/ssh/..", true},
		{"/usr", true},
		{"/var", true},
		{"/home", true},
		{"/root", true},
		{"/var/lib/postgresql", true},
		{"/var/lib/mysql", true},
		{"/tmp/restore", false},
		{"/var/lib/sentinel/restore", false},
		{"/home/alice/restore", false},
		{"/etcetera", false},
		{"/mnt/restore", false},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			if got := IsSystemRestoreTarget(tt.dir); got != tt.want {
				t.Errorf("IsSystemRestoreTarget(%q) = %v, want %v", tt.dir, got, tt.want)
			}
		})
	}
}
