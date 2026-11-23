package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/config"
)

// fakeServer builds a small server tree under a temporary folder and points the suggestions at it.
func fakeServer(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"etc", "var/www", "srv", "home/alice", "home/bob", "home/.hidden", "home/lost+found", "opt"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "home", "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRoots, oldHome := suggestionRoots, homeRoot
	suggestionRoots = []string{filepath.Join(root, "etc"), filepath.Join(root, "var/www"), filepath.Join(root, "srv"), filepath.Join(root, "missing"), filepath.Join(root, "opt")}
	homeRoot = filepath.Join(root, "home")
	t.Cleanup(func() { suggestionRoots, homeRoot = oldRoots, oldHome })
	return root
}

func TestSuggestFoldersKeepsWhatExistsAndIsNotInTheFieldYet(t *testing.T) {
	root := fakeServer(t)
	got := suggestFolders(nil)
	want := []string{"etc", "var/www", "srv", "opt", "home/alice", "home/bob"}
	if len(got) != len(want) {
		t.Fatalf("suggestions = %v", got)
	}
	for i, folder := range want {
		if got[i] != filepath.Join(root, folder) {
			t.Errorf("suggestion %d = %q, want %q", i, got[i], filepath.Join(root, folder))
		}
	}
	t.Setenv("SENTINEL_SERVER", root)
	have := []string{filepath.Join(root, "etc") + "/", "${SENTINEL_SERVER}/srv"}
	for _, folder := range suggestFolders(have) {
		if strings.HasSuffix(folder, "/etc") || strings.HasSuffix(folder, "/srv") {
			t.Errorf("%q is in the field already (trailing slash and ${VAR} are normalised)", folder)
		}
	}
}

func TestSuggestFoldersStopsAtTheLimitAndSkipsUnreadableFolders(t *testing.T) {
	root := fakeServer(t)
	for i := range 12 {
		if err := os.MkdirAll(filepath.Join(root, "home", "user"+string(rune('a'+i))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := suggestFolders(nil); len(got) != maxSuggestions {
		t.Errorf("%d suggestions, the line is capped at %d", len(got), maxSuggestions)
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(root, "srv")
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		for _, folder := range suggestFolders(nil) {
			if folder == locked {
				t.Error("a folder that cannot be listed is not worth suggesting")
			}
		}
	}
}

func TestAddPathAddsOnceKeepingTheOrder(t *testing.T) {
	if got := addPath("/a, /b", "/c"); got != "/a, /b, /c" {
		t.Errorf("addPath = %q", got)
	}
	if got := addPath("/a,/b", "/a"); got != "/a, /b" {
		t.Errorf("a repeated folder is not added twice: %q", got)
	}
	if got := addPath("", "/x"); got != "/x" {
		t.Errorf("addPath to an empty field = %q", got)
	}
}

// pathsForm is the add-a-job form with the files type chosen and the Paths field focused.
func pathsForm(t *testing.T) manageModel {
	t.Helper()
	screen, _ := formModel(t)
	screen = openAddForm(t, screen)
	screen.form.field("type").value = config.JobFiles
	screen.form.focus = fieldIndex(screen, "paths")
	return screen
}

func TestPathsFieldShowsSuggestionsAndAddsThemByKeyAndByClick(t *testing.T) {
	root := fakeServer(t)
	screen := pathsForm(t)
	screen.width = 300 // the temporary folders have long names
	view := plain(screen.render())
	if !strings.Contains(view, "thêm: 1 "+filepath.Join(root, "etc")) || !strings.Contains(view, "2 "+filepath.Join(root, "var/www")) {
		t.Fatalf("the suggestions are not under the focused Paths field:\n%s", view)
	}

	screen = keys(t, screen, "alt+2")
	if got := screen.form.field("paths").value; got != filepath.Join(root, "var/www") {
		t.Fatalf("alt+2 adds the second suggestion: %q", got)
	}
	if view := plain(screen.render()); strings.Contains(view, "2 "+filepath.Join(root, "var/www")) && strings.Contains(view, "thêm: 1 "+filepath.Join(root, "etc")+"  2 "+filepath.Join(root, "var/www")) {
		t.Errorf("a folder in the field is no longer suggested:\n%s", view)
	}

	screen, _ = clickOn(t, screen, "suggest:"+itoa(fieldIndex(screen, "paths"))+":1")
	if got := screen.form.field("paths").value; got != filepath.Join(root, "var/www")+", "+filepath.Join(root, "etc") {
		t.Errorf("clicking the first suggestion adds the first one that is left: %q", got)
	}
	screen = keys(t, screen, "alt+9", "alt+0", "alt+x") // out of range or not a number: nothing happens
	if got := len(splitList(screen.form.field("paths").value)); got != 2 {
		t.Errorf("%d paths after the useless keys", got)
	}
}

func TestSuggestionsShowOnlyOnTheFocusedPathsFieldAndOnlyForFilesJobs(t *testing.T) {
	fakeServer(t)
	screen := pathsForm(t)
	screen.form.focus = fieldIndex(screen, "name")
	if view := plain(screen.render()); strings.Contains(view, "thêm: 1") {
		t.Errorf("suggestions belong to the focused Paths field:\n%s", view)
	}
	screen.form.field("type").value = config.JobPostgres
	screen.form.focus = fieldIndex(screen, "database")
	if view := plain(screen.render()); strings.Contains(view, "thêm: 1") || strings.Contains(view, "Đường dẫn") {
		t.Errorf("a database job has no Paths:\n%s", view)
	}
}

func TestInitFormSuggestsFoldersForTheFirstJob(t *testing.T) {
	root := fakeServer(t)
	program, _ := initScreen(t, false)
	program.form.focus = fieldIndex(program.manageModel, "job_paths")
	program.width = 300
	if view := plain(program.render()); !strings.Contains(view, "thêm: 1 "+filepath.Join(root, "etc")) {
		t.Errorf("init suggests folders too:\n%s", view)
	}
	program = pressInit(t, program, "alt+1")
	if got := program.form.field("job_paths").value; !strings.HasSuffix(got, filepath.Join(root, "etc")) {
		t.Errorf("job_paths = %q", got)
	}
}

func TestSuggestionTargetsStayOnTheScreenAndDoNotOverlap(t *testing.T) {
	fakeServer(t)
	screen := pathsForm(t)
	model, _ := screen.Update(tea5Size(44))
	narrow := model.(manageModel)
	narrow.render()
	var chips []clickTarget
	for _, target := range narrow.hits.targets {
		if strings.HasPrefix(target.action, "suggest:") {
			chips = append(chips, target)
		}
	}
	if len(chips) == 0 {
		t.Fatal("no suggestion fits a 44-column screen: the first one must be shortened")
	}
	for i, one := range chips {
		if one.to > 44 {
			t.Errorf("target %+v goes past a 44-column screen", one)
		}
		for _, other := range chips[i+1:] {
			if one.row == other.row && one.from < other.to && other.from < one.to {
				t.Errorf("%+v overlaps %+v", one, other)
			}
		}
	}
}

func itoa(num int) string { return strconv.Itoa(num) }

func tea5Size(width int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: width, Height: 30} }
