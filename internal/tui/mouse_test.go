package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// click presses the left button at a screen position. The main screen has no frame; the other
// screens sit inside one, which moves their content two columns and one row.
func click(t *testing.T, screen manageModel, col, row int) (manageModel, tea.Cmd) {
	t.Helper()
	x, y := col, row
	if !screen.onTabs() {
		x, y = col+2, row+1
	}
	model, cmd := screen.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return model.(manageModel), cmd
}

func wheel(t *testing.T, m manageModel, button tea.MouseButton) manageModel {
	t.Helper()
	model, _ := m.Update(tea.MouseWheelMsg{Button: button})
	return model.(manageModel)
}

// screenRow returns the text of one screen row, without colours.
func screenRow(m manageModel, y int) string {
	rows := strings.Split(plain(m.render()), "\n")
	if m.onTabs() {
		return rows[y]
	}
	if y+2 >= len(rows) { // the top and bottom border lines are not content rows
		return ""
	}
	text := []rune(rows[y+1])
	return string(text[2 : len(text)-2]) // without the frame's side borders
}

// targetFor renders the screen and returns the click target with that action.
func targetFor(t *testing.T, m manageModel, action string) clickTarget {
	t.Helper()
	m.render()
	for _, target := range m.hits.targets {
		if target.action == action {
			return target
		}
	}
	t.Fatalf("no click target %q on the screen: %+v", action, m.hits.targets)
	return clickTarget{}
}

// clickOn clicks the middle of the target with that action.
func clickOn(t *testing.T, m manageModel, action string) (manageModel, tea.Cmd) {
	t.Helper()
	target := targetFor(t, m, action)
	return click(t, m, (target.from+target.to)/2, target.row)
}

func TestMouseTargetsLineUpWithTheScreen(t *testing.T) {
	m := loadedModel(t)
	if row := screenRow(m, targetFor(t, m, "tab:0").row); !strings.Contains(row, "1 Backups") || !strings.Contains(row, "2 Uptime") {
		t.Errorf("the tab targets are not on the tab bar: %q", row)
	}
	if row := screenRow(m, targetFor(t, m, "row:0").row); !strings.Contains(row, "appdb") {
		t.Errorf("row 0 is not the first job: %q", row)
	}
	if row := screenRow(m, targetFor(t, m, "row:2").row); !strings.Contains(row, "mongo") {
		t.Errorf("row 2 is not the third job: %q", row)
	}
	if row := screenRow(m, targetFor(t, m, "b").row); !strings.Contains(row, "b backup") {
		t.Errorf("the b chip is not in the footer: %q", row)
	}

	m, _ = clickOn(t, m, "tab:1")
	if m.tab != 1 || m.cursor != 0 {
		t.Fatalf("clicking the second tab: tab %d cursor %d", m.tab, m.cursor)
	}
	if row := screenRow(m, targetFor(t, m, "row:1").row); !strings.Contains(row, "api") {
		t.Errorf("the Uptime rows are targets too: %q", row)
	}
	m, _ = clickOn(t, m, "tab:0")
	if m.tab != 0 {
		t.Fatalf("clicking the first tab: tab %d", m.tab)
	}
	m, _ = click(t, m, 1, 1) // the summary line
	if m.tab != 0 {
		t.Error("only the tab bar row switches tabs")
	}

	m, cmd := clickOn(t, m, "row:1")
	if m.cursor != 1 || cmd != nil {
		t.Fatalf("clicking a line selects it: cursor %d", m.cursor)
	}
	if !strings.Contains(plain(m.render()), "files → local") {
		t.Errorf("the detail should follow the click:\n%s", plain(m.render()))
	}
}

func TestMouseTargetsFollowAScrolledTableAndNarrowFooter(t *testing.T) {
	m := loadedModel(t)
	model, _ := m.Update(tea.WindowSizeMsg{Width: 44, Height: 14}) // 3 visible rows
	m = model.(manageModel)
	m.cursor = 2
	for _, action := range []string{"row:0", "row:1", "row:2", "b", "s", "q"} {
		target := targetFor(t, m, action)
		if target.to > 44 {
			t.Errorf("target %q reaches column %d on a 44-column screen", action, target.to)
		}
	}
	if m2, cmd := clickOn(t, modelWithNarrow(t), "s"); !isBusy(m2) || cmd == nil {
		t.Errorf("clicking the s chip lists the snapshots (screens %d)", len(m2.screens))
	}
}

// modelWithNarrow is a 44-column screen with working actions.
func modelWithNarrow(t *testing.T) manageModel {
	t.Helper()
	model, _ := modelWith(t, &fakeActions{}).Update(tea.WindowSizeMsg{Width: 44, Height: 14})
	return model.(manageModel)
}

func TestMouseFooterChipsPressTheirKeys(t *testing.T) {
	m := loadedModel(t)
	m, _ = clickOn(t, m, "?")
	if _, ok := m.top().(helpScreen); !ok {
		t.Fatalf("the ? chip opens the help (screens %d)", len(m.screens))
	}
	m, _ = clickOn(t, m, "esc") // "any key" closes
	if !m.onTabs() {
		t.Fatalf("clicking the help footer closes it (screens %d)", len(m.screens))
	}
	m, cmd := clickOn(t, m, "q")
	if !m.quitting || cmd == nil {
		t.Error("the q chip quits")
	}
}

func TestSelectedLineHasABackground(t *testing.T) {
	m := loadedModel(t)
	rows := strings.Split(m.render(), "\n")
	selected := rows[targetFor(t, m, "row:0").row]
	other := rows[targetFor(t, m, "row:1").row]
	if !strings.Contains(selected, "48;2;48;51;61") { // the dark palette's selection colour
		t.Errorf("the selected line has no background: %q", selected)
	}
	if strings.Contains(other, "48;2;48;51;61") {
		t.Errorf("only the selected line has the background: %q", other)
	}
	if width := len([]rune(plain(selected))) - frameCols; width != m.width {
		t.Errorf("the background covers %d of %d columns", width, m.width)
	}
}

func TestMouseSecondClickOpensTheJobSnapshots(t *testing.T) {
	fake := &fakeActions{}
	m := modelWith(t, fake)
	m, _ = clickOn(t, m, "row:1")
	if !m.onTabs() || m.cursor != 1 {
		t.Fatal("a click on another line only selects it")
	}
	m, cmd := clickOn(t, m, "row:1")
	if !isBusy(m) || cmd == nil {
		t.Fatalf("a second click on the selected job opens it (screens %d)", len(m.screens))
	}
	m = feed(t, m, messagesOf(cmd))
	if _, ok := openScreen[snapshotsScreen](m); !ok {
		t.Fatalf("snapshot list missing (screens %d)", len(m.screens))
	}

	if row := screenRow(m, targetFor(t, m, "snap:0").row); !strings.Contains(row, "newest00") {
		t.Fatalf("snapshot 0 is not at its target row: %q\n%s", row, plain(m.render()))
	}
	m, _ = clickOn(t, m, "snap:1")
	if list, _ := openScreen[snapshotsScreen](m); list.cursor != 1 {
		t.Errorf("clicking a snapshot selects it: %d", list.cursor)
	}
	m, _ = clickOn(t, m, "snap:1")
	if asked, ok := openScreen[targetScreen](m); !ok || asked.snap.ID != "older00000000000" {
		t.Errorf("a second click starts the restore (screens %d, chosen %q)", len(m.screens), asked.snap.ID)
	}
}

func TestMouseWheelScrollsAndStaysInRange(t *testing.T) {
	m := loadedModel(t)
	m = wheel(t, m, tea.MouseWheelDown)
	if m.cursor != 2 { // three lines on the Backups tab: 0 + 3 is clamped to the last
		t.Errorf("wheel down: cursor %d", m.cursor)
	}
	m = wheel(t, m, tea.MouseWheelUp)
	if m.cursor != 0 {
		t.Errorf("wheel up: cursor %d", m.cursor)
	}
}

func TestMouseIsIgnoredWhileTypingAndForOtherButtons(t *testing.T) {
	m := loadedModel(t)
	tabTarget := targetFor(t, m, "tab:1")
	model, _ := m.Update(tea.MouseClickMsg{X: tabTarget.from + 2, Y: tabTarget.row + 1, Button: tea.MouseRight})
	if model.(manageModel).tab != 0 {
		t.Error("only the left button clicks")
	}
	withConfig, _ := formModel(t)
	form, _ := withConfig.onKey("a", "")
	typing := form.(manageModel)
	before := typing.form.focus
	typing, _ = click(t, typing, 5, 0) // the blank line under the frame
	if typing.mode != modeForm || typing.form.focus != before {
		t.Error("a click must not touch the form fields")
	}
	typing, _ = clickOn(t, typing, "esc")
	if typing.mode != modeTable {
		t.Error("the cancel chip of the form works with the mouse")
	}
}

func TestViewAsksForMouseEvents(t *testing.T) {
	if loadedModel(t).View().MouseMode != tea.MouseModeCellMotion {
		t.Error("the view must turn the mouse on")
	}
}

func TestHelpOpensFromTheJobsAndSnapshotsAndClosesOnAnyKey(t *testing.T) {
	m := loadedModel(t)
	model, _ := m.onKey("?", "?")
	m = model.(manageModel)
	view := plain(m.render())
	for _, want := range []string{"Phím", "backup job đang chọn", "thêm job", "click"} {
		if !strings.Contains(view, want) {
			t.Errorf("help lacks %q:\n%s", want, view)
		}
	}
	model, _ = m.onKey("x", "x")
	if model.(manageModel).mode != modeTable {
		t.Error("any key closes the help")
	}

	m2 := modelWith(t, &fakeActions{})
	model, cmd := m2.onKey("s", "")
	m2 = feed(t, model.(manageModel), messagesOf(cmd))
	model, _ = m2.onKey("?", "?")
	m2 = model.(manageModel)
	if view := plain(m2.render()); !strings.Contains(view, "Snapshots") || !strings.Contains(view, "restore snapshot đang chọn") {
		t.Errorf("snapshot help:\n%s", view)
	}
	model, _ = m2.onKey("esc", "")
	if _, ok := openScreen[snapshotsScreen](model.(manageModel)); !ok {
		t.Error("closing the help returns to the snapshot list")
	}
}

func TestHelpKeyIsTextInForms(t *testing.T) {
	m, _ := formModel(t)
	model, _ := m.onKey("a", "")
	model, _ = model.(manageModel).onKey("tab", "")
	model, _ = model.(manageModel).onKey("?", "?")
	typing := model.(manageModel)
	if typing.mode != modeForm || typing.form.field("name").value != "?" {
		t.Errorf("? typed into a form field is text (mode %d, name %q)", typing.mode, typing.form.field("name").value)
	}
}

func TestFooterMentionsHelp(t *testing.T) {
	if !strings.Contains(plain(loadedModel(t).render()), "? trợ giúp") {
		t.Error("the footer should point to the help")
	}
}

func TestClickTargetsNeverOverlap(t *testing.T) {
	for _, width := range []int{44, 80, 120} {
		m := loadedModel(t)
		model, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m = model.(manageModel)
		for tab := range m.tabs() {
			m.tab = tab
			m.render()
			targets := m.hits.targets
			for i, one := range targets {
				for _, other := range targets[i+1:] {
					if one.row == other.row && one.from < other.to && other.from < one.to {
						t.Errorf("width %d, tab %d: %+v overlaps %+v", width, tab, one, other)
					}
				}
			}
		}
	}
}

func TestClickingTheDimRowUnderALineSelectsThatLine(t *testing.T) {
	m := loadedModel(t) // the failed job "files" (line 1) has its error under its row
	m.render()
	var rows []int
	for _, target := range m.hits.targets {
		if target.action == "row:1" {
			rows = append(rows, target.row)
		}
	}
	if len(rows) != 2 || rows[1] != rows[0]+1 {
		t.Fatalf("a line with a dim row has two click rows, one under the other: %v", rows)
	}
	if row := screenRow(m, rows[1]); !strings.Contains(row, "└ restic exited 1") {
		t.Fatalf("the second row is not the dim row: %q", row)
	}
	m, _ = click(t, m, 10, rows[1])
	if m.cursor != 1 {
		t.Errorf("clicking the dim row selects its line: cursor %d", m.cursor)
	}
}
