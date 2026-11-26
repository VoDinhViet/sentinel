package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const wheelStep = 3

// clickTarget is a stretch of one screen row that a click acts on. The renderers record
// them while they write the lines, so the positions can never drift from what is drawn.
type clickTarget struct {
	row, from, to int // columns from..to-1 of screen row `row`
	action        string
}

// hitMap collects the click targets of the last render. It is shared by every copy of the
// model; render rebuilds it, and so does a click, so it is never stale.
type hitMap struct {
	targets []clickTarget
	offset  int // screen row of the first line the current renderer writes
}

func (h *hitMap) reset() {
	if h != nil {
		h.targets, h.offset = h.targets[:0], 0
	}
}

// add records a target on a row counted from the first line of the current renderer.
func (h *hitMap) add(screenRow, from, width int, action string) {
	if h != nil && action != "" && width > 0 {
		h.targets = append(h.targets, clickTarget{row: h.offset + screenRow, from: from, to: from + width, action: action})
	}
}

// shift moves the targets added since index from by cols columns to the right.
func (h *hitMap) shift(from, cols int) {
	if h == nil {
		return
	}
	for i := from; i < len(h.targets); i++ {
		h.targets[i].from += cols
		h.targets[i].to += cols
	}
}

func (h *hitMap) safeTargets() []clickTarget {
	if h == nil {
		return nil
	}
	return h.targets
}

// moveRow moves the targets of screen row from to row to (the key bar, pinned to the bottom).
func (h *hitMap) moveRow(from, to int) {
	if h == nil {
		return
	}
	for i := range h.targets {
		if h.targets[i].row == from {
			h.targets[i].row = to
		}
	}
}

// truncate forgets the targets recorded after the first n.
func (h *hitMap) truncate(count int) {
	if h != nil && count <= len(h.targets) {
		h.targets = h.targets[:count]
	}
}

func (h *hitMap) at(col, row int) (string, bool) {
	if h == nil {
		return "", false
	}
	for _, target := range h.targets {
		if target.row == row && col >= target.from && col < target.to {
			return target.action, true
		}
	}
	return "", false
}

// firstVisible is the index of the first line of a list that is on screen.
func firstVisible(cursor, visible int) int {
	if cursor >= visible {
		return cursor - visible + 1
	}
	return 0
}

// Click actions that are not a key: "tab:2", "row:5" and "snap:1".
func actionIndex(action, prefix string) (int, bool) {
	number, found := strings.CutPrefix(action, prefix)
	index, err := strconv.Atoi(number)
	return index, found && err == nil
}

func (m manageModel) onClick(col, row int, left bool) (tea.Model, tea.Cmd) {
	if !left {
		return m, nil
	}
	m.render() // rebuild the targets for what is on screen now
	action, ok := m.clickedAction(col, row)
	if !ok {
		return m, nil
	}
	if tab, ok := actionIndex(action, "tab:"); ok {
		if tab != m.tab {
			m.tab, m.cursor, m.notice = tab, 0, ""
		}
		return m, nil
	}
	if index, ok := actionIndex(action, "row:"); ok {
		if index == m.cursor && (m.tab == backupsTab || m.tab == configTab) {
			return m.onTableKey("s") // a second click opens the job, or the entry to edit
		}
		m.cursor, m.notice = index, ""
		return m, nil
	}
	if index, ok := actionIndex(action, "snap:"); ok {
		if list, ok := m.top().(snapshotsScreen); ok {
			return list.click(m, index)
		}
		return m, nil
	}
	if m.mode == modeBrowse {
		if next, cmd, handled := m.browseClick(action); handled {
			return next, cmd
		}
	}
	if m.mode == modeForm && action == "browse" {
		next, cmd := m.openBrowse()
		return next, cmd
	}
	if m.mode == modeForm {
		before := m.form.connectionKey()
		if next, handled := m.formAction(action); handled {
			if next.form.connectionKey() != before {
				next.form.connect = connectState{}
			}
			return next, nil
		}
	}
	return m.onKey(action, "")
}

// clickedAction is the target under a click. The main screen has no frame; the others sit inside
// one, so the click is moved past its border and the space after it, and past the top border.
func (m manageModel) clickedAction(col, row int) (string, bool) {
	if m.onTabs() {
		return m.hits.at(col, row)
	}
	return m.hits.at(col-2, row-1)
}

// onWheel moves the selection by wheelStep lines in the current list.
func (m manageModel) onWheel(up, down bool) manageModel {
	step := 0
	switch {
	case up:
		step = -wheelStep
	case down:
		step = wheelStep
	}
	if list, ok := m.top().(snapshotsScreen); ok {
		return list.scroll(m, step)
	}
	if len(m.screens) > 0 {
		return m
	}
	switch m.mode {
	case modeTable:
		m.cursor = min(max(m.cursor+step, 0), max(len(m.tabs()[m.tab].lines)-1, 0))
	}
	return m
}
