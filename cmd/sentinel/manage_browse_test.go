package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// browseTree makes a small tree to browse:
//
//	alpha/  beta/nginx-sites/  beta/deep/uploads/  www/(3000 bytes)  .hidden/  locked/  link -> www  file.txt
func browseTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"alpha", "beta/nginx-sites", "beta/deep/uploads", "www", ".hidden", "locked"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for file, size := range map[string]int{"file.txt": 5, "www/index.html": 1000, "www/app.js": 2000} {
		if err := os.WriteFile(filepath.Join(root, file), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "www"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(filepath.Join(root, "locked"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(filepath.Join(root, "locked"), 0o755) })
	}
	return root
}

// openBrowser focuses the Paths field with that value and presses ctrl+o.
func openBrowser(t *testing.T, value string) (manageModel, tea.Cmd) {
	t.Helper()
	screen := pathsForm(t)
	model, _ := screen.Update(tea5Size(100))
	screen = model.(manageModel)
	screen.form.field("paths").value = value
	model2, cmd := screen.onKey("ctrl+o", "")
	return model2.(manageModel), cmd
}

func typeFilter(t *testing.T, screen manageModel, text string) manageModel {
	t.Helper()
	for _, r := range text {
		model, _ := screen.onKey(string(r), string(r))
		screen = model.(manageModel)
	}
	return screen
}

func rowNames(screen manageModel) []string {
	var names []string
	for _, row := range screen.browse.rows() {
		names = append(names, row.name)
	}
	return names
}

func TestBrowserStartsBesideTheLastPathAndListsOnlyFolders(t *testing.T) {
	root := browseTree(t)
	screen, cmd := openBrowser(t, "/etc/hosts, "+filepath.Join(root, "www"))
	if screen.mode != modeBrowse || screen.browse.dir != root {
		t.Fatalf("mode %d dir %q, want the folder that holds the last path (%s)", screen.mode, screen.browse.dir, root)
	}
	want := []string{"..", "alpha/", "beta/", "link/", "locked/", "www/", ".hidden/"}
	if got := rowNames(screen); !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v (folders only, hidden last)", got, want)
	}
	if rows := screen.browse.rows(); rows[screen.browse.cursor].name != "www/" {
		t.Errorf("the cursor should be on the last path, not %q", rows[screen.browse.cursor].name)
	}
	if cmd == nil {
		t.Error("the size of the selected folder is counted in the background")
	}
	view := plain(screen.render())
	for _, want := range []string{"Choose folders to back up", "0 chosen", "Search:", "› [ ] www/", "alpha/"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "file.txt") {
		t.Error("files are not listed")
	}
}

func TestBrowserStartsAtTheRootWithAnEmptyFieldAndAtTheNearestFolderForAMissingPath(t *testing.T) {
	root := browseTree(t)
	if screen, _ := openBrowser(t, ""); screen.browse.dir != "/" {
		t.Errorf("an empty field starts at /, not %q", screen.browse.dir)
	}
	if screen, _ := openBrowser(t, filepath.Join(root, "beta", "gone", "deeper")); screen.browse.dir != filepath.Join(root, "beta") {
		t.Errorf("a missing path starts at its nearest folder, not %q", screen.browse.dir)
	}
	if screen, _ := openBrowser(t, "relative/dir"); screen.browse.dir != "/" {
		t.Errorf("a relative path says nothing about where to start: %q", screen.browse.dir)
	}
}

func TestBrowserSizeEstimateArrivesLaterAndIsCached(t *testing.T) {
	root := browseTree(t)
	screen, cmd := openBrowser(t, filepath.Join(root, "www"))
	if view := plain(screen.render()); !strings.Contains(view, "…") {
		t.Errorf("a folder being counted shows …:\n%s", view)
	}
	msg, ok := cmd().(folderSizedMsg)
	if !ok || msg.bytes != 3000 || !msg.complete {
		t.Fatalf("size message = %+v", msg)
	}
	model, _ := screen.Update(msg)
	screen = model.(manageModel)
	if view := plain(screen.render()); !strings.Contains(view, "≈ 2.9 KiB") {
		t.Errorf("the estimate is not shown:\n%s", view)
	}
	screen = keys(t, screen, "up")
	screen = keys(t, screen, "down")
	if _, cmd := screen.browseMove(0); cmd != nil {
		t.Error("a folder that is counted is not counted again")
	}
}

func TestDirSizeStopsAtTheLimitsAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	for i := range 5 {
		if err := os.WriteFile(filepath.Join(root, "f"+string(rune('a'+i))), make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if bytes, complete := dirSize(root, 100, time.Second); bytes != 500 || !complete {
		t.Errorf("dirSize = %d, %v; a symlink loop is not followed", bytes, complete)
	}
	if bytes, complete := dirSize(root, 3, time.Second); complete || bytes > 300 {
		t.Errorf("past the file limit the figure is a lower bound: %d, %v", bytes, complete)
	}
	if _, complete := dirSize(root, 100, -time.Second); complete {
		t.Error("past the time limit the figure is a lower bound")
	}
	screen := manageModel{browse: browseState{sizes: map[string]folderSize{"/x": {bytes: 2 << 30, done: true}}}}
	if note := screen.browse.sizeNote(browseRow{kind: browseFolder, path: "/x"}); note != "> 2.0 GiB" {
		t.Errorf("an incomplete estimate says so: %q", note)
	}
}

func TestBrowserOpensFoldersAndGoesBackUp(t *testing.T) {
	root := browseTree(t)
	screen, _ := openBrowser(t, filepath.Join(root, "www"))
	screen = keys(t, screen, "up", "up", "up") // www, locked, link, beta
	if rows := screen.browse.rows(); rows[screen.browse.cursor].name != "beta/" {
		t.Fatalf("cursor on %q", rows[screen.browse.cursor].name)
	}
	screen = keys(t, screen, "enter")
	if screen.browse.dir != filepath.Join(root, "beta") || !slices.Equal(rowNames(screen), []string{"..", "deep/", "nginx-sites/"}) {
		t.Fatalf("dir %q rows %v", screen.browse.dir, rowNames(screen))
	}
	screen = keys(t, screen, "left")
	if rows := screen.browse.rows(); screen.browse.dir != root || rows[screen.browse.cursor].name != "beta/" {
		t.Errorf("← goes up and stays on the folder just left: %q on %q", screen.browse.dir, rows[screen.browse.cursor].name)
	}
	screen = keys(t, screen, "right", "backspace")
	if screen.browse.dir != root {
		t.Errorf("backspace with no filter goes up: %q", screen.browse.dir)
	}
	screen = keys(t, screen, "up", "enter") // ".." is first when the cursor is high enough
	if screen.browse.dir == "" {
		t.Error("lost the folder")
	}
}

func TestBrowserFilterNarrowsTheListAndSearchesDeeper(t *testing.T) {
	root := browseTree(t)
	screen, _ := openBrowser(t, filepath.Join(root, "www"))
	screen = typeFilter(t, screen, "B")
	if got := rowNames(screen); !slices.Equal(got, []string{"..", "beta/"}) {
		t.Errorf("one letter filters by name, ignoring case, and does not search deeper: %v", got)
	}
	screen = keys(t, screen, "backspace")
	screen = typeFilter(t, screen, "upl")
	if got := rowNames(screen); !slices.Equal(got, []string{"..", "beta/deep/uploads/"}) {
		t.Errorf("rows = %v", got)
	}
	view := plain(screen.render())
	for _, want := range []string{"Search: upl", "also found deeper:", "beta/deep/uploads/"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	screen = keys(t, screen, "enter")
	if screen.browse.dir != filepath.Join(root, "beta/deep/uploads") || screen.browse.filter != "" {
		t.Errorf("a deeper match opens straight into the folder: %q", screen.browse.dir)
	}
	screen = typeFilter(t, keys(t, screen, "left", "left", "left"), "zzz")
	if view := plain(screen.render()); !strings.Contains(view, `no folder matches "zzz"`) {
		t.Errorf("nothing matched and the screen does not say so:\n%s", view)
	}
}

func TestDeepSearchLimits(t *testing.T) {
	root := t.TempDir()
	mk := func(path string) {
		if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("l1/l2/l3/needle4")
	mk("l1/l2/l3/l4/needle5")
	mk("l1/.hidden/needle-hidden")
	mk("real/needle-real")
	mk("skipme/needle-skipped")
	for i := range 20 {
		mk("many/match" + string(rune('a'+i)))
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	oldSkip := deepSkip
	deepSkip = []string{filepath.Join(root, "skipme")}
	t.Cleanup(func() { deepSkip = oldSkip })

	found := deepSearch(root, "needle")
	relative := map[string]bool{}
	for _, path := range found {
		rel, _ := filepath.Rel(root, path)
		relative[rel] = true
	}
	want := map[string]bool{"l1/l2/l3/needle4": true, "real/needle-real": true}
	if len(relative) != len(want) || !relative["l1/l2/l3/needle4"] || !relative["real/needle-real"] {
		t.Errorf("found %v: 4 levels deep yes, 5 no, hidden no, symlinks and skipped trees not entered", relative)
	}
	if got := deepSearch(root, "match"); len(got) != maxDeepResults {
		t.Errorf("%d results, capped at %d", len(got), maxDeepResults)
	}
	if got := deepSearch(root, "needle-real"); len(got) != 1 {
		t.Errorf("the folder behind a symlink is found once, not twice: %v", got)
	}
}

func TestBrowserMarksAcrossFoldersAndCtrlSJoinsThemWithoutRepeats(t *testing.T) {
	root := browseTree(t)
	screen, _ := openBrowser(t, "/etc, "+filepath.Join(root, "alpha"))
	screen = keys(t, screen, "space") // alpha is under the cursor
	screen = keys(t, screen, "down", "enter", "down", "space")
	// beta was opened with the cursor on deep/; one down is nginx-sites/
	if got := screen.browse.marks; !slices.Equal(got, []string{filepath.Join(root, "alpha"), filepath.Join(root, "beta/nginx-sites")}) {
		t.Fatalf("marks = %v", got)
	}
	if view := plain(screen.render()); !strings.Contains(view, "2 chosen") || !strings.Contains(view, "[✓] nginx-sites/") {
		t.Errorf("marks are not shown:\n%s", view)
	}
	screen = keys(t, screen, "space")
	if len(screen.browse.marks) != 1 {
		t.Errorf("space again takes the mark off: %v", screen.browse.marks)
	}
	screen = keys(t, screen, "space", "ctrl+s")
	if screen.mode != modeForm {
		t.Fatalf("ctrl+s returns to the form (mode %d)", screen.mode)
	}
	want := "/etc, " + filepath.Join(root, "alpha") + ", " + filepath.Join(root, "beta/nginx-sites")
	if got := screen.form.field("paths").value; got != want {
		t.Errorf("paths = %q, want %q (alpha was in the field already)", got, want)
	}
}

func TestBrowserEscKeepsTheFieldAsItWas(t *testing.T) {
	root := browseTree(t)
	screen, _ := openBrowser(t, filepath.Join(root, "www"))
	screen = keys(t, screen, "up", "space", "esc")
	if screen.mode != modeForm || screen.form.field("paths").value != filepath.Join(root, "www") {
		t.Errorf("esc cancels: mode %d, paths %q", screen.mode, screen.form.field("paths").value)
	}
}

func TestBrowserDoesNotEnterOrMarkAFolderItCannotList(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can list every folder")
	}
	root := browseTree(t)
	screen, _ := openBrowser(t, filepath.Join(root, "locked"))
	if view := plain(screen.render()); !strings.Contains(view, "✗ no access") {
		t.Errorf("a folder that cannot be listed says so:\n%s", view)
	}
	screen = keys(t, screen, "enter", "space")
	if screen.browse.dir != root || len(screen.browse.marks) != 0 {
		t.Errorf("entered or marked a locked folder: %q %v", screen.browse.dir, screen.browse.marks)
	}
}

func TestBrowserMouse(t *testing.T) {
	root := browseTree(t)
	screen := pathsForm(t)
	model, _ := screen.Update(tea5Size(100))
	screen = model.(manageModel)
	screen.form.field("paths").value = filepath.Join(root, "www")
	screen, _ = clickOn(t, screen, "browse")
	if screen.mode != modeBrowse {
		t.Fatalf("the [ Browse ] chip opens the browser (mode %d)", screen.mode)
	}
	rows := screen.browse.rows()
	alpha := slices.IndexFunc(rows, func(row browseRow) bool { return row.name == "alpha/" })
	action := "bro:" + itoa(alpha)

	screen, _ = clickOn(t, screen, action)
	if screen.browse.cursor != alpha || screen.browse.dir != root {
		t.Errorf("the first click selects: cursor %d dir %q", screen.browse.cursor, screen.browse.dir)
	}
	screen, _ = clickOn(t, screen, "bmark:"+itoa(alpha))
	if !screen.browse.marked(filepath.Join(root, "alpha")) {
		t.Error("the tick box marks the folder")
	}
	screen = wheel(t, screen, tea.MouseWheelDown)
	if screen.browse.cursor != min(alpha+wheelStep, len(rows)-1) {
		t.Errorf("the wheel moves the cursor: %d", screen.browse.cursor)
	}
	screen, _ = clickOn(t, screen, "bro:"+itoa(screen.browse.cursor))
	if screen.browse.dir == root {
		t.Error("a click on the selected row opens it")
	}
	screen, _ = clickOn(t, screen, "ctrl+s")
	if screen.mode != modeForm || !strings.Contains(screen.form.field("paths").value, filepath.Join(root, "alpha")) {
		t.Errorf("the done chip applies the choice: mode %d paths %q", screen.mode, screen.form.field("paths").value)
	}
}

func TestBrowserClickTargetsStayOnTheScreenAndDoNotOverlap(t *testing.T) {
	root := browseTree(t)
	for _, width := range []int{44, 100} {
		screen, _ := openBrowser(t, filepath.Join(root, "www"))
		model, _ := screen.Update(tea5Size(width))
		screen = model.(manageModel)
		screen.render()
		targets := screen.hits.targets
		if len(targets) == 0 {
			t.Fatalf("width %d: no targets", width)
		}
		for i, one := range targets {
			if one.to > width {
				t.Errorf("width %d: %+v goes past the screen", width, one)
			}
			for _, other := range targets[i+1:] {
				if one.row == other.row && one.from < other.to && other.from < one.to && !strings.HasPrefix(one.action, "bmark:") && !strings.HasPrefix(other.action, "bmark:") {
					t.Errorf("width %d: %+v overlaps %+v", width, one, other)
				}
			}
		}
		// A tick box sits on top of its row: the box wins, so it must be recorded first.
		var boxRow int
		for _, target := range targets {
			if strings.HasPrefix(target.action, "bmark:") {
				boxRow = target.row
				if action, _ := screen.hits.at(target.from, target.row); action != target.action {
					t.Errorf("width %d: a click on the tick box acts as %q", width, action)
				}
			}
		}
		if boxRow == 0 {
			t.Errorf("width %d: no tick box target", width)
		}
	}
}

func TestInitWizardKeepsRunningWhileBrowsing(t *testing.T) {
	root := browseTree(t)
	program, _ := initScreen(t, false)
	program.form.focus = fieldIndex(program.manageModel, "job_paths")
	program.form.field("job_paths").value = filepath.Join(root, "www")
	program = pressInit(t, program, "ctrl+o")
	if program.mode != modeBrowse {
		t.Fatalf("ctrl+o does not open the browser in the init form (mode %d)", program.mode)
	}
	program = pressInit(t, program, "space")
	program = pressInit(t, program, "ctrl+s")
	if program.mode != modeForm || !strings.Contains(program.form.field("job_paths").value, filepath.Join(root, "www")) {
		t.Errorf("mode %d paths %q", program.mode, program.form.field("job_paths").value)
	}
}

func TestCtrlOOnAnotherFieldDoesNothing(t *testing.T) {
	screen := pathsForm(t)
	screen.form.focus = fieldIndex(screen, "name")
	if screen = keys(t, screen, "ctrl+o"); screen.mode != modeForm {
		t.Errorf("ctrl+o only browses from a Paths field (mode %d)", screen.mode)
	}
}
