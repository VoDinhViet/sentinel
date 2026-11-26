package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"sentinel/internal/backup"
	"sentinel/internal/termtext"
)

type browseKind int

const (
	browseUp     browseKind = iota // ".." : the parent folder
	browseFolder                   // a folder of the current one
	browseDeeper                   // a folder found by the search, further down
)

// browseRow is one line of the folder browser.
type browseRow struct {
	kind   browseKind
	name   string // as shown
	path   string
	hidden bool // a dot folder
	denied bool // cannot be listed by this user
}

// folderSize is the estimate of one folder; done is false while it is being counted.
type folderSize struct {
	bytes    int64
	complete bool
	done     bool
}

// folderSizedMsg carries the size estimate of a folder, counted in the background.
type folderSizedMsg struct {
	path     string
	bytes    int64
	complete bool
}

// browseState is the folder browser opened from a Paths field.
type browseState struct {
	field   int    // the form field the choice is added to
	dir     string // the folder being listed
	filter  string // typed text: filters the list and searches deeper
	folders []browseRow
	deeper  []browseRow
	cursor  int
	marks   []string // chosen folders, in the order they were chosen; kept across folders
	sizes   map[string]folderSize
}

// folderRows lists the folders directly under dir: symlinks to a folder count, hidden ones come last.
func folderRows(dir string) []browseRow {
	children, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var visible, hidden []browseRow
	for _, child := range children {
		path := filepath.Join(dir, child.Name())
		if !child.IsDir() {
			if fileInfo, err := os.Stat(path); child.Type()&os.ModeSymlink == 0 || err != nil || !fileInfo.IsDir() {
				continue
			}
		}
		row := browseRow{kind: browseFolder, name: termtext.Clean(child.Name()) + "/", path: path, hidden: strings.HasPrefix(child.Name(), "."), denied: !readableDir(path)}
		if row.hidden {
			hidden = append(hidden, row)
		} else {
			visible = append(visible, row)
		}
	}
	byName := func(rows []browseRow) {
		sort.SliceStable(rows, func(i, j int) bool { return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name) })
	}
	byName(visible)
	byName(hidden)
	return append(visible, hidden...)
}

// rows is what the list shows: "..", the folders that match the filter, then the deeper matches.
func (b browseState) rows() []browseRow {
	var rows []browseRow
	if b.dir != "/" {
		rows = append(rows, browseRow{kind: browseUp, name: "..", path: filepath.Dir(b.dir)})
	}
	needle := strings.ToLower(b.filter)
	for _, folder := range b.folders {
		if strings.Contains(strings.ToLower(folder.name), needle) {
			rows = append(rows, folder)
		}
	}
	return append(rows, b.deeper...)
}

// firstFolder is the index of the first row that is not "..".
func (b browseState) firstFolder() int {
	rows := b.rows()
	for i, row := range rows {
		if row.kind != browseUp {
			return i
		}
	}
	return 0
}

func (b browseState) marked(path string) bool {
	for _, mark := range b.marks {
		if mark == path {
			return true
		}
	}
	return false
}

// nearestFolder is the closest folder at or above path that can be listed.
func nearestFolder(path string) string {
	for !readableDir(path) {
		parent := filepath.Dir(path)
		if parent == path {
			return "/"
		}
		path = parent
	}
	return path
}

// openBrowse starts the browser from the focused Paths field, in the folder that holds its last path.
func (m manageModel) openBrowse() (manageModel, tea.Cmd) {
	m.browse = browseState{field: m.form.focus, sizes: map[string]folderSize{}}
	m.mode = modeBrowse
	start, focus := "/", ""
	if paths := splitList(m.form.fields[m.form.focus].value); len(paths) > 0 {
		if last := filepath.Clean(os.ExpandEnv(paths[len(paths)-1])); filepath.IsAbs(last) {
			start, focus = nearestFolder(filepath.Dir(last)), last
		}
	}
	return m.browseTo(start, focus)
}

// browseTo lists dir and puts the cursor on focus, or on the first folder.
func (m manageModel) browseTo(dir, focus string) (manageModel, tea.Cmd) {
	m.browse.dir, m.browse.filter, m.browse.deeper = dir, "", nil
	m.browse.folders = folderRows(dir)
	m.browse.cursor = m.browse.firstFolder()
	for i, row := range m.browse.rows() {
		if row.path == focus {
			m.browse.cursor = i
		}
	}
	return m, m.sizeSelected()
}

// sizeSelected starts counting the selected folder unless that is done or under way.
func (m manageModel) sizeSelected() tea.Cmd {
	rows := m.browse.rows()
	if m.browse.cursor >= len(rows) {
		return nil
	}
	row := rows[m.browse.cursor]
	if _, known := m.browse.sizes[row.path]; known || row.kind == browseUp || row.denied {
		return nil
	}
	m.browse.sizes[row.path] = folderSize{}
	return func() tea.Msg {
		bytes, complete := dirSize(row.path, maxSizeFiles, maxSizeTime)
		return folderSizedMsg{path: row.path, bytes: bytes, complete: complete}
	}
}

func (m manageModel) onFolderSized(msg folderSizedMsg) manageModel {
	if m.browse.sizes != nil {
		m.browse.sizes[msg.path] = folderSize{bytes: msg.bytes, complete: msg.complete, done: true}
	}
	return m
}

// browseMove moves the cursor by delta rows.
func (m manageModel) browseMove(delta int) (manageModel, tea.Cmd) {
	m.browse.cursor = min(max(m.browse.cursor+delta, 0), max(len(m.browse.rows())-1, 0))
	return m, m.sizeSelected()
}

// browseOpen goes into the selected folder (or up for "..").
func (m manageModel) browseOpen() (manageModel, tea.Cmd) {
	rows := m.browse.rows()
	if m.browse.cursor >= len(rows) || rows[m.browse.cursor].denied {
		return m, nil
	}
	return m.browseTo(rows[m.browse.cursor].path, "")
}

// browseUp goes to the parent folder and keeps the cursor on the folder just left.
func (m manageModel) browseUp() (manageModel, tea.Cmd) {
	if m.browse.dir == "/" {
		return m, nil
	}
	return m.browseTo(filepath.Dir(m.browse.dir), m.browse.dir)
}

// browseMark adds the selected folder to the chosen ones, or takes it out.
func (m manageModel) browseMark() manageModel {
	rows := m.browse.rows()
	if m.browse.cursor >= len(rows) || rows[m.browse.cursor].kind == browseUp || rows[m.browse.cursor].denied {
		return m
	}
	path := rows[m.browse.cursor].path
	var marks []string
	for _, mark := range m.browse.marks {
		if mark != path {
			marks = append(marks, mark)
		}
	}
	if len(marks) == len(m.browse.marks) {
		marks = append(marks, path)
	}
	m.browse.marks = marks
	return m
}

// browseFilter changes the filter text and searches deeper once it has two characters.
func (m manageModel) browseFilter(filter string) (manageModel, tea.Cmd) {
	m.browse.filter, m.browse.deeper = filter, nil
	if len([]rune(filter)) >= 2 {
		for _, path := range deepSearch(m.browse.dir, filter) {
			relative, err := filepath.Rel(m.browse.dir, path)
			if err != nil {
				continue
			}
			m.browse.deeper = append(m.browse.deeper, browseRow{kind: browseDeeper, name: termtext.Clean(relative) + "/", path: path, denied: !readableDir(path)})
		}
	}
	m.browse.cursor = m.browse.firstFolder()
	return m, m.sizeSelected()
}

// browseDone adds the chosen folders to the Paths field and returns to the form.
func (m manageModel) browseDone() manageModel {
	field := &m.form.fields[m.browse.field]
	for _, folder := range m.browse.marks {
		field.value = addPath(field.value, folder)
	}
	if len(m.browse.marks) > 0 {
		m.form.touched[field.key] = true
	}
	m.form.problem = ""
	m.mode = modeForm
	return m
}

func (m manageModel) onBrowseKey(key, text string) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch key {
	case "esc", "ctrl+c":
		m.mode = modeForm
	case "ctrl+s":
		m = m.browseDone()
	case "up":
		m, cmd = m.browseMove(-1)
	case "down":
		m, cmd = m.browseMove(1)
	case "enter", "right":
		m, cmd = m.browseOpen()
	case "left":
		m, cmd = m.browseUp()
	case "backspace":
		if m.browse.filter == "" {
			m, cmd = m.browseUp()
		} else {
			m, cmd = m.browseFilter(editText(m.browse.filter, key, text))
		}
	case "space", " ":
		m = m.browseMark()
	default:
		if text != "" && key != "enter" {
			m, cmd = m.browseFilter(m.browse.filter + text)
		}
	}
	return m, cmd
}

// browseWheel scrolls the list.
func (m manageModel) browseWheel(up, down bool) (tea.Model, tea.Cmd) {
	switch {
	case up:
		return m.browseMove(-wheelStep)
	case down:
		return m.browseMove(wheelStep)
	}
	return m, nil
}

// browseClick handles "bro:<row>" (select, or open when already selected) and "bmark:<row>".
func (m manageModel) browseClick(action string) (tea.Model, tea.Cmd, bool) {
	if index, ok := actionIndex(action, "bro:"); ok {
		if index == m.browse.cursor {
			next, cmd := m.browseOpen()
			return next, cmd, true
		}
		next, cmd := m.browseMove(index - m.browse.cursor)
		return next, cmd, true
	}
	if index, ok := actionIndex(action, "bmark:"); ok {
		next, cmd := m.browseMove(index - m.browse.cursor)
		return next.browseMark(), cmd, true
	}
	return m, nil, false
}

// sizeNote is the estimate shown at the end of a folder's line.
func (b browseState) sizeNote(row browseRow) string {
	if row.denied {
		return "✗ không có quyền"
	}
	size, known := b.sizes[row.path]
	switch {
	case !known || row.kind == browseUp:
		return ""
	case !size.done:
		return "…"
	case size.complete:
		return "≈ " + backup.HumanBytes(size.bytes)
	}
	return "> " + backup.HumanBytes(size.bytes)
}

func (m manageModel) renderBrowse() []string {
	browse := m.browse
	const heading = "Chọn thư mục để backup"
	title := titleStyle.Render(heading)
	chosen := fmt.Sprintf("đã chọn %d", len(browse.marks))
	where := ""
	if room := m.width - lipgloss.Width(heading) - lipgloss.Width(chosen) - 10; room >= 12 {
		where = tailOf(browse.dir, room) + "  ·  " // too narrow a screen drops the folder: the last line shows it
	}
	screenLines := []string{
		title + dimStyle.Render("  "+where+chosen),
		"Tìm: " + browse.filter + lipglossAccent("▏"),
	}
	rows := browse.rows()
	folders := 0
	for _, row := range rows {
		if row.kind != browseUp {
			folders++
		}
	}
	visible := max(m.height-9, 3)
	first := firstVisible(browse.cursor, visible)
	deeperShown := false
	for i := first; i < min(first+visible, len(rows)); i++ {
		row := rows[i]
		if row.kind == browseDeeper && !deeperShown {
			screenLines = append(screenLines, dimStyle.Render("  còn thấy ở sâu hơn:"))
			deeperShown = true
		}
		screenLines = append(screenLines, m.browseLine(len(screenLines), i, row))
	}
	if folders == 0 {
		message := "không có thư mục nào ở đây"
		if browse.filter != "" {
			message = "không có thư mục nào khớp \"" + browse.filter + "\""
		}
		screenLines = append(screenLines, dimStyle.Render("  "+message))
	}
	if hidden := len(rows) - (first + visible); hidden > 0 {
		screenLines = append(screenLines, dimStyle.Render(fmt.Sprintf("  … còn %d", hidden)))
	}
	if browse.cursor < len(rows) {
		screenLines = append(screenLines, "", dimStyle.Render(tailOf(rows[browse.cursor].path, m.width-2)))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "↑↓", "chọn", "space", "đánh dấu", "enter", "mở", "←", "lên", "ctrl+s", "xong", "esc", "huỷ"))
}

// browseLine draws one row: marker, tick box, name and the note at the right edge.
func (m manageModel) browseLine(screenRow, index int, row browseRow) string {
	browse := m.browse
	box := "   "
	if row.kind != browseUp && !row.denied {
		box = "[ ]"
		if browse.marked(row.path) {
			box = "[✓]"
		}
		m.hits.add(screenRow, 2, 3, fmt.Sprintf("bmark:%d", index))
	}
	m.hits.add(screenRow, 0, m.width, fmt.Sprintf("bro:%d", index))
	selected := index == browse.cursor
	marker := "  "
	if selected {
		marker = "› "
	}
	note := browse.sizeNote(row)
	name := truncate(row.name, max(m.width-lipgloss.Width(note)-10, 8))
	left := marker + box + " " + name
	gap := strings.Repeat(" ", max(m.width-lipgloss.Width(left)-lipgloss.Width(note)-1, 1))
	switch {
	case selected:
		text := left + gap + note
		return selectedStyle.Render(text + strings.Repeat(" ", max(m.width-lipgloss.Width(text), 0)))
	case row.denied:
		return left + gap + severityStyle(sevBad).Render(note)
	case row.hidden:
		return dimStyle.Render(left + gap + note)
	}
	return left + gap + dimStyle.Render(note)
}

// tailOf keeps the end of a long path: …/last/folders.
func tailOf(path string, width int) string {
	runes := []rune(path)
	if len(runes) <= width || width < 2 {
		return path
	}
	return "…" + string(runes[len(runes)-(width-1):])
}
