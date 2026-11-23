package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func click(t *testing.T, m manageModel, x, y int) (manageModel, tea.Cmd) {
	t.Helper()
	model, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
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
	if y >= len(rows) {
		return ""
	}
	return rows[y]
}

func TestMouseTabBarAndTableRowsLineUpWithTheScreen(t *testing.T) {
	m := loadedModel(t)
	if !strings.Contains(screenRow(m, tabBarRow), "1 Backups") || !strings.Contains(screenRow(m, firstTableRow), "appdb") {
		t.Fatalf("the geometry constants do not match the screen:\ntab bar row: %q\nfirst line: %q", screenRow(m, tabBarRow), screenRow(m, firstTableRow))
	}

	m, _ = click(t, m, 16, tabBarRow) // "2 Uptime"
	if m.tab != 1 || m.cursor != 0 {
		t.Fatalf("clicking the second tab: tab %d cursor %d", m.tab, m.cursor)
	}
	m, _ = click(t, m, 1, tabBarRow) // "1 Backups"
	if m.tab != 0 {
		t.Fatalf("clicking the first tab: tab %d", m.tab)
	}
	m, _ = click(t, m, 1, tabBarRow-1) // the summary line
	if m.tab != 0 {
		t.Error("only the tab bar row switches tabs")
	}

	m, cmd := click(t, m, 5, firstTableRow+1) // the second job
	if m.cursor != 1 || cmd != nil {
		t.Fatalf("clicking a line selects it: cursor %d", m.cursor)
	}
	if !strings.Contains(plain(m.render()), "files → local") {
		t.Errorf("the detail should follow the click:\n%s", plain(m.render()))
	}
}

func TestMouseSecondClickOpensTheJobSnapshots(t *testing.T) {
	fake := &fakeActions{}
	m := modelWith(t, fake)
	m, _ = click(t, m, 5, firstTableRow+1)
	if m.mode != modeTable || m.cursor != 1 {
		t.Fatal("a click on another line only selects it")
	}
	m, cmd := click(t, m, 5, firstTableRow+1)
	if m.mode != modeBusy || cmd == nil {
		t.Fatalf("a second click on the selected job opens it (mode %d)", m.mode)
	}
	m = feed(t, m, messagesOf(cmd))
	if m.mode != modeSnapshots {
		t.Fatalf("snapshot list missing (mode %d)", m.mode)
	}

	if !strings.Contains(screenRow(m, snapshotListTop), "newest00") {
		t.Fatalf("snapshot rows do not start at row %d:\n%s", snapshotListTop, plain(m.render()))
	}
	m, _ = click(t, m, 5, snapshotListTop+1)
	if m.snapshotCursor != 1 {
		t.Errorf("clicking a snapshot selects it: %d", m.snapshotCursor)
	}
	m, _ = click(t, m, 5, snapshotListTop+1)
	if m.mode != modeTarget || m.chosen.ID != "older00000000000" {
		t.Errorf("a second click starts the restore (mode %d, chosen %q)", m.mode, m.chosen.ID)
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
	model, _ := m.Update(tea.MouseClickMsg{X: 16, Y: tabBarRow, Button: tea.MouseRight})
	if model.(manageModel).tab != 0 {
		t.Error("only the left button clicks")
	}
	withConfig, _ := formModel(t)
	form, _ := withConfig.onKey("a", "")
	typing := form.(manageModel)
	before := typing.form.focus
	typing, _ = click(t, typing, 5, firstTableRow)
	if typing.mode != modeForm || typing.form.focus != before {
		t.Error("a click must not touch the form")
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
	for _, want := range []string{"Keys", "back up the selected job now", "add a database job", "click"} {
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
	if view := plain(m2.render()); !strings.Contains(view, "Snapshots") || !strings.Contains(view, "restore the selected snapshot") {
		t.Errorf("snapshot help:\n%s", view)
	}
	model, _ = m2.onKey("esc", "")
	if model.(manageModel).mode != modeSnapshots {
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
	if !strings.Contains(plain(loadedModel(t).render()), "? help") {
		t.Error("the footer should point to the help")
	}
}
