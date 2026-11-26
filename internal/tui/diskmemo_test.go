package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDiskMemoReadsTheDiskOncePerAnswer(t *testing.T) {
	root := t.TempDir()
	etc, srv := filepath.Join(root, "etc"), filepath.Join(root, "srv")
	for _, folder := range []string{etc, srv} {
		if err := os.Mkdir(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldRoots, oldHome := suggestionRoots, homeRoot
	suggestionRoots, homeRoot = []string{etc, srv}, filepath.Join(root, "no-home")
	t.Cleanup(func() { suggestionRoots, homeRoot = oldRoots, oldHome })

	memo := &diskMemo{}
	first := memo.suggestFolders(nil)
	if !slices.Equal(first, []string{etc, srv}) {
		t.Fatalf("suggestions = %v", first)
	}
	if err := os.Remove(srv); err != nil {
		t.Fatal(err)
	}
	if again := memo.suggestFolders(nil); !slices.Equal(again, first) {
		t.Errorf("the suggestions changed under the open form: %v", again)
	}
	if left := memo.suggestFolders([]string{etc}); !slices.Equal(left, []string{srv}) {
		t.Errorf("a folder already in the field must still be left out, got %v", left)
	}

	missing := filepath.Join(root, "missing")
	if note := plain(memo.folderNote(missing)); !strings.Contains(note, "sẽ được tạo") {
		t.Fatalf("note = %q", note)
	}
	if err := os.Mkdir(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	if note := plain(memo.folderNote(missing)); !strings.Contains(note, "sẽ được tạo") {
		t.Errorf("the note was read again: %q", note)
	}
	memo.forget()
	if note := plain(memo.folderNote(missing)); !strings.Contains(note, "đã có, ghi được") {
		t.Errorf("a field entered again must be looked at again: %q", note)
	}
	if again := memo.suggestFolders(nil); !slices.Equal(again, first) {
		t.Errorf("forget must keep the suggestions: %v", again)
	}
}

func TestNilDiskMemoReadsEveryTime(t *testing.T) {
	var memo *diskMemo
	dir := t.TempDir()
	if note := plain(memo.pathsNote(dir)); !strings.Contains(note, "có trên máy này") {
		t.Errorf("note = %q", note)
	}
	memo.forget() // must not panic
}
