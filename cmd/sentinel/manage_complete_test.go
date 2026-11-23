package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCompletePath(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"docs", "docs-old", "photos", ".hidden", "solo/inner"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "photos"), filepath.Join(root, "pics")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPLETE_ROOT", root)
	t.Setenv("HOME", root)

	tests := []struct {
		name, token, want string
		options           []string
	}{
		{"one folder match gets a slash", root + "/ph", root + "/photos/", nil},
		{"one file match has no slash", root + "/no", root + "/notes.txt", nil},
		{"several matches fill the common prefix", root + "/d", root + "/docs", []string{"docs-old/", "docs/"}},
		{"nothing matches", root + "/zzz", root + "/zzz", nil},
		{"a symlink to a folder is a folder", root + "/pi", root + "/pics/", nil},
		{"hidden names only when asked for", root + "/.h", root + "/.hidden/", nil},
		{"${VAR} is kept as typed", "${COMPLETE_ROOT}/ph", "${COMPLETE_ROOT}/photos/", nil},
		{"~ is kept as typed", "~/so", "~/solo/", nil},
		{"~ alone steps into the folder", "~", "~/", nil},
		{"a trailing slash lists the folder", root + "/solo/", root + "/solo/inner/", nil},
		{"empty stays empty", "", "", nil},
		{"a relative path is left alone", "docs", "docs", nil},
		{"a missing folder is left alone", root + "/nope/x", root + "/nope/x", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, options := completePath(tt.token)
			if got != tt.want {
				t.Errorf("completePath(%q) = %q, want %q", tt.token, got, tt.want)
			}
			if tt.options != nil && !slices.Equal(slices.Sorted(slices.Values(options)), slices.Sorted(slices.Values(tt.options))) {
				t.Errorf("options = %v, want %v", options, tt.options)
			}
		})
	}
}

func TestCompletePathCapsTheOptions(t *testing.T) {
	root := t.TempDir()
	for i := range maxOptions + 5 {
		if err := os.Mkdir(filepath.Join(root, "item"+string(rune('a'+i))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	completed, options := completePath(root + "/i")
	if completed != root+"/item" || len(options) != maxOptions+1 || options[maxOptions] != "+5 more" {
		t.Errorf("completed %q, options %v", completed, options)
	}
}

func TestCompleteListFinishesOnlyTheLastItem(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var", "www"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, _ := completeList("/etc, "+root+"/v", true)
	if want := "/etc, " + root + "/var/"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, _ := completeList(root+"/va, x", false); got != root+"/va, x" {
		t.Errorf("a single-path field is one token: %q", got)
	}
}
