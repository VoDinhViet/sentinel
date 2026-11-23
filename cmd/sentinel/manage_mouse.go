package main

import (
	tea "charm.land/bubbletea/v2"

	"charm.land/lipgloss/v2"
)

// Screen rows (y, from 0) of the jobs screen: title, summary and a blank line, then the tab
// bar, its rule and the column header, then the first table line.
const (
	tabBarRow       = 3
	firstTableRow   = 6
	snapshotListTop = 5 // title and blank, then "Snapshots of", a blank and the column header
	wheelStep       = 3
)

// firstVisible is the index of the first table line that is on screen.
func firstVisible(cursor, visible int) int {
	if cursor >= visible {
		return cursor - visible + 1
	}
	return 0
}

// tabAt is the tab under column x of the tab bar, or -1.
func (m manageModel) tabAt(x int) int {
	start := 0
	for i, label := range m.tabLabels() {
		end := start + lipgloss.Width(label)
		if x >= start && x < end {
			return i
		}
		start = end + lipgloss.Width(tabGap)
	}
	return -1
}

// tableTop is the row of the first table line; an error message above the table pushes it down.
func (m manageModel) tableTop() int {
	if m.loadErr != nil {
		return firstTableRow + 2
	}
	return firstTableRow
}

func (m manageModel) onClick(x, y int, left bool) (tea.Model, tea.Cmd) {
	if !left {
		return m, nil
	}
	switch m.mode {
	case modeTable:
		if y == tabBarRow {
			if tab := m.tabAt(x); tab >= 0 && tab != m.tab {
				m.tab, m.cursor, m.notice = tab, 0, ""
			}
			return m, nil
		}
		lines := m.tabs()[m.tab].lines
		index := firstVisible(m.cursor, m.visibleRows()) + y - m.tableTop()
		if y < m.tableTop() || index < 0 || index >= len(lines) || index >= firstVisible(m.cursor, m.visibleRows())+m.visibleRows() {
			return m, nil
		}
		if index == m.cursor && m.tab == 0 {
			return m.onTableKey("s") // a second click opens the job
		}
		m.cursor, m.notice = index, ""
	case modeSnapshots:
		index := firstVisible(m.snapshotCursor, max(m.height-8, 3)) + y - snapshotListTop
		if y < snapshotListTop || index < 0 || index >= len(m.snapshots) {
			return m, nil
		}
		if index == m.snapshotCursor {
			return m.onSnapshotsKey("enter")
		}
		m.snapshotCursor = index
	}
	return m, nil
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
