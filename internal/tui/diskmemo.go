package tui

// The form shows what is on this machine under some fields (does the folder exist, is it writable, which
// folders are worth adding). Reading that is a few system calls per path, and a form is drawn at every key,
// so the answers are kept: a note for as long as the field holds the same text and has the focus, the
// suggestions for as long as the form is open. On a hung network mount that is one wait, not one per key.
type diskMemo struct {
	folderNotes  map[string]string
	pathsNotes   map[string]string
	candidates   []string
	candidatesOK bool
}

// forget drops the notes, so that a field entered again is looked at again. The suggestions stay, so
// that the number a key adds is the number the screen showed.
func (d *diskMemo) forget() {
	if d != nil {
		d.folderNotes, d.pathsNotes = nil, nil
	}
}

// folderNote is folderNote(folder), read once per text.
func (d *diskMemo) folderNote(folder string) string {
	if d == nil {
		return folderNote(folder)
	}
	if note, ok := d.folderNotes[folder]; ok {
		return note
	}
	note := folderNote(folder)
	if d.folderNotes == nil {
		d.folderNotes = map[string]string{}
	}
	d.folderNotes[folder] = note
	return note
}

// pathsNote is pathsNote(pathsText), read once per text.
func (d *diskMemo) pathsNote(pathsText string) string {
	if d == nil {
		return pathsNote(pathsText)
	}
	if note, ok := d.pathsNotes[pathsText]; ok {
		return note
	}
	note := pathsNote(pathsText)
	if d.pathsNotes == nil {
		d.pathsNotes = map[string]string{}
	}
	d.pathsNotes[pathsText] = note
	return note
}

// suggestFolders is suggestFolders(have), with the folders of this machine listed once.
func (d *diskMemo) suggestFolders(have []string) []string {
	if d == nil {
		return suggestFolders(have)
	}
	if !d.candidatesOK {
		d.candidates, d.candidatesOK = suggestionCandidates(), true
	}
	return pickSuggestions(d.candidates, have)
}
