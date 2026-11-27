package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// sendKey sends a key to the program as bubbletea does, as a message, not as a name the test made up.
func sendKey(t *testing.T, screen manageModel, key tea.KeyPressMsg) manageModel {
	t.Helper()
	model, _ := screen.Update(key)
	return model.(manageModel)
}

func TestSpaceBarTicksAChecklistItemAsBubbleteaSendsIt(t *testing.T) {
	space := tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	if space.String() != "space" {
		t.Fatalf("bubbletea names the space bar %q; this test and the code assume %q", space.String(), "space")
	}
	screen, _ := formModel(t)
	screen = tabTo(t, openAddForm(t, screen), func(field formField) bool { return field.key == "storage" })
	screen = keys(t, screen, "right", "right") // automatic -> folder -> repository: the repositories are listed
	screen = tabTo(t, screen, func(field formField) bool { return field.kind == fieldChecklist })
	index := screen.form.focus
	before := screen.form.fields[index].ticked[0]

	screen = sendKey(t, screen, space)
	if screen.form.fields[index].ticked[0] == before {
		t.Error("the space bar did not tick the item under the cursor")
	}
}

func TestSpaceBarTypesASpaceInATextFieldAsBubbleteaSendsIt(t *testing.T) {
	screen := firstText(t, "backup")
	screen = sendKey(t, screen, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	screen = typeText(t, screen, "web")
	if got := focusedValue(screen); got != "backup web" {
		t.Errorf("value = %q", got)
	}
}
