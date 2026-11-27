package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// The text fields of a form keep their text in formField.value and the place of the cursor in
// formField.back, the number of characters after it, so a field whose text is set by the program
// (a completed path, a suggestion) has its cursor at the end. The editing itself is the textinput of
// charm.land/bubbles: the arrows, home and end move the cursor, ctrl+w, ctrl+u, ctrl+k and delete
// remove text, and what is typed or pasted goes in at the cursor.

// editAt applies a key press or a paste to text with the cursor back characters before its end, and
// returns the new text and cursor.
func editAt(text string, back int, msg tea.Msg) (string, int) {
	input := textinput.New()
	input.Focus()
	input.SetValue(text)
	input.SetCursor(len([]rune(text)) - clampBack(text, back))
	input, _ = input.Update(msg)
	return input.Value(), len([]rune(input.Value())) - input.Position()
}

// clampBack keeps the cursor inside text, whatever the text has become since it was placed.
func clampBack(text string, back int) int {
	return min(max(back, 0), len([]rune(text)))
}

// withCursor is text with the cursor drawn at its place.
func withCursor(text string, back int) string {
	runes := []rune(text)
	at := len(runes) - clampBack(text, back)
	return string(runes[:at]) + lipglossAccent("▏") + string(runes[at:])
}

// keyPress is the message bubbletea would have sent for the key whose name and text the manager
// gets ("ctrl+w", "left", "a"): textinput looks at the message, not at the name.
func keyPress(key, text string) tea.KeyPressMsg {
	if text != "" {
		return tea.KeyPressMsg{Code: []rune(text)[0], Text: text}
	}
	var mod tea.KeyMod
	for {
		switch {
		case strings.HasPrefix(key, "ctrl+"):
			mod |= tea.ModCtrl
			key = strings.TrimPrefix(key, "ctrl+")
		case strings.HasPrefix(key, "alt+"):
			mod |= tea.ModAlt
			key = strings.TrimPrefix(key, "alt+")
		default:
			if code, ok := keyCodes[key]; ok {
				return tea.KeyPressMsg{Code: code, Mod: mod}
			}
			if runes := []rune(key); len(runes) == 1 {
				return tea.KeyPressMsg{Code: runes[0], Mod: mod}
			}
			return tea.KeyPressMsg{}
		}
	}
}

var keyCodes = map[string]rune{
	"left": tea.KeyLeft, "right": tea.KeyRight, "home": tea.KeyHome, "end": tea.KeyEnd,
	"delete": tea.KeyDelete, "backspace": tea.KeyBackspace,
}

// editsText says that a field takes a cursor and the editing keys: text, number and folder list.
func (f formField) editsText() bool {
	switch f.kind {
	case fieldText, fieldNumber, fieldPaths:
		return !f.fixed
	}
	return false
}

// edit applies a key or a paste to the field. A number field takes digits only.
func (f *formField) edit(msg tea.Msg) {
	if press, ok := msg.(tea.KeyPressMsg); ok && f.kind == fieldNumber && press.Text != "" && strings.Trim(press.Text, "0123456789") != "" {
		return
	}
	if paste, ok := msg.(tea.PasteMsg); ok && f.kind == fieldNumber {
		msg = tea.PasteMsg{Content: strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, paste.Content)}
	}
	f.value, f.back = editAt(f.value, f.back, msg)
}
