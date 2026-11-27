package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// tabTo moves the focus to the next field for which want is true.
func tabTo(t *testing.T, screen manageModel, want func(formField) bool) manageModel {
	t.Helper()
	for range screen.form.fields {
		if want(screen.form.fields[screen.form.focus]) {
			return screen
		}
		screen = keys(t, screen, "tab")
	}
	t.Fatal("the form has no such field")
	return screen
}

// firstText opens the add form, goes to its first text field and types text into it.
func firstText(t *testing.T, text string) manageModel {
	t.Helper()
	screen, _ := formModel(t)
	screen = tabTo(t, openAddForm(t, screen), formField.editsText)
	return typeText(t, screen, text)
}

func focusedValue(screen manageModel) string { return screen.form.fields[screen.form.focus].value }

func TestEditingKeysMoveAndDeleteInsideATextField(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		typ  string // typed after the keys
		want string
	}{
		{"typing goes in at the cursor", []string{"left", "left", "left"}, "X", "webX-01"},
		{"home goes to the start", []string{"home"}, "X", "Xweb-01"},
		{"end goes back to the end", []string{"home", "end"}, "X", "web-01X"},
		{"backspace deletes before the cursor", []string{"left", "left", "backspace"}, "", "web01"},
		{"delete removes after the cursor", []string{"home", "delete"}, "", "eb-01"},
		{"ctrl+u deletes up to the start", []string{"left", "left", "ctrl+u"}, "", "01"},
		{"ctrl+k deletes to the end", []string{"home", "right", "right", "right", "ctrl+k"}, "", "web"},
		{"ctrl+a is home", []string{"ctrl+a"}, "X", "Xweb-01"},
		{"a space goes in at the cursor", []string{"left", "left"}, " ", "web- 01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			screen := firstText(t, "web-01")
			screen = keys(t, screen, tc.keys...)
			screen = typeText(t, screen, tc.typ)
			if got := focusedValue(screen); got != tc.want {
				t.Errorf("value = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCursorGoesToTheEndWhenTheFieldIsLeftAndComesBack(t *testing.T) {
	screen := firstText(t, "web")
	screen = keys(t, screen, "home", "tab", "shift+tab")
	screen = typeText(t, screen, "X")
	if got := focusedValue(screen); got != "webX" {
		t.Errorf("value = %q, want the new letter at the end", got)
	}
}

func TestTheCursorIsDrawnWhereItIs(t *testing.T) {
	screen := firstText(t, "web")
	screen = keys(t, screen, "left")
	if view := plain(screen.render()); !strings.Contains(view, "we▏b") {
		t.Errorf("the cursor is not between e and b:\n%s", view)
	}
}

func TestPasteGoesIntoTheTextFieldAtTheCursor(t *testing.T) {
	screen := firstText(t, "web01")
	screen = keys(t, screen, "left", "left")
	model, _ := screen.Update(tea.PasteMsg{Content: "-prod"})
	if got := focusedValue(model.(manageModel)); got != "web-prod01" {
		t.Errorf("value = %q", got)
	}
}

func TestPasteOutsideATextFieldDoesNothing(t *testing.T) {
	model, _ := loadedModel(t).Update(tea.PasteMsg{Content: "x"})
	if !model.(manageModel).onTabs() {
		t.Error("a paste on the tabs opened something")
	}
	screen, _ := formModel(t)
	screen = tabTo(t, openAddForm(t, screen), func(field formField) bool { return field.kind == fieldChoice })
	before := focusedValue(screen)
	model, _ = screen.Update(tea.PasteMsg{Content: "zzz"})
	if got := focusedValue(model.(manageModel)); got != before {
		t.Errorf("a paste changed a choice from %q to %q", before, got)
	}
}

func TestNumberFieldTakesOnlyDigitsAtTheCursorAndFromAPaste(t *testing.T) {
	screen, _ := formModel(t)
	screen = openAddForm(t, screen)
	screen = keys(t, screen, "tab", "tab", "tab", "tab") // port
	screen = typeText(t, screen, "5432")
	screen = keys(t, screen, "left", "left")
	screen = typeText(t, screen, "x9")
	if got := screen.form.field("port").value; got != "54932" {
		t.Errorf("port = %q", got)
	}
	model, _ := screen.Update(tea.PasteMsg{Content: "a1b2"})
	if got := model.(manageModel).form.field("port").value; got != "5491232" {
		t.Errorf("port after a paste = %q", got)
	}
}

func TestArrowsStillStepAChoiceField(t *testing.T) {
	screen, _ := formModel(t)
	screen = tabTo(t, openAddForm(t, screen), func(field formField) bool { return field.kind == fieldChoice && !field.fixed })
	before := focusedValue(screen)
	screen = keys(t, screen, "right")
	if got := focusedValue(screen); got == before {
		t.Errorf("→ did not change the choice %q", before)
	}
}

func TestCtrlWDeletesTheWordBeforeTheCursor(t *testing.T) {
	screen := firstText(t, "backup web")
	if got := focusedValue(keys(t, screen, "ctrl+w")); got != "backup " {
		t.Errorf("value = %q, want one word gone", got)
	}
}
