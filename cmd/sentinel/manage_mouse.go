package main

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
func (h *hitMap) add(row, from, width int, action string) {
	if h != nil && action != "" && width > 0 {
		h.targets = append(h.targets, clickTarget{row: h.offset + row, from: from, to: from + width, action: action})
	}
}

func (h *hitMap) safeTargets() []clickTarget {
	if h == nil {
		return nil
	}
	return h.targets
}

// truncate forgets the targets recorded after the first n.
func (h *hitMap) truncate(n int) {
	if h != nil && n <= len(h.targets) {
		h.targets = h.targets[:n]
	}
}

func (h *hitMap) at(x, y int) (string, bool) {
	if h == nil {
		return "", false
	}
	for _, target := range h.targets {
		if target.row == y && x >= target.from && x < target.to {
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

func (m manageModel) onClick(x, y int, left bool) (tea.Model, tea.Cmd) {
	if !left {
		return m, nil
	}
	m.render() // rebuild the targets for what is on screen now
	action, ok := m.hits.at(x, y)
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
		if index == m.cursor && m.tab == 0 {
			return m.onTableKey("s") // a second click opens the job
		}
		m.cursor, m.notice = index, ""
		return m, nil
	}
	if index, ok := actionIndex(action, "snap:"); ok {
		if index == m.snapshotCursor {
			return m.onSnapshotsKey("enter")
		}
		m.snapshotCursor = index
		return m, nil
	}
	return m.onKey(action, "")
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
	switch m.mode {
	case modeTable:
		m.cursor = min(max(m.cursor+step, 0), max(len(m.tabs()[m.tab].lines)-1, 0))
	case modeSnapshots:
		m.snapshotCursor = min(max(m.snapshotCursor+step, 0), max(len(m.snapshots)-1, 0))
	}
	return m
}
