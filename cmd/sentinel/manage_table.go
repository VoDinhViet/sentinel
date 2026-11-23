package main

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	columnGap = 2
	rowPrefix = 4 // the selection marker and the status dot in front of every row
	// keptColumnWidth is the room a column needs to stay on a narrow screen; columns that would
	// get less are dropped, last first, instead of being squeezed until nothing is readable.
	keptColumnWidth = 9
)

// tableRow is one screen row of a table. line is the index of the manageLine it belongs to; a
// heading belongs to the first line under it.
type tableRow struct {
	text    string
	line    int
	heading bool
}

// layout is the sections of a tab: its own, or one section without heading that holds every line.
func (tab manageTab) layout() []tableSection {
	if len(tab.sections) > 0 {
		return tab.sections
	}
	return []tableSection{{headers: tab.headers}}
}

// linesOf are the lines of the n-th section of layout.
func (tab manageTab) linesOf(sections []tableSection, n int) []manageLine {
	end := len(tab.lines)
	if n+1 < len(sections) {
		end = sections[n+1].first
	}
	return tab.lines[sections[n].first:end]
}

// renderTable writes the rows of the table that fit in capacity rows and keeps the selected line in
// view. firstRow is the screen row of the first one; every line is a click target.
func (m manageModel) renderTable(tab manageTab, inner, capacity, firstRow int) []string {
	allRows := m.tableRows(tab, inner)
	var pinned []string
	if len(tab.sections) == 0 { // a plain table keeps its column header in place
		pinned, allRows = []string{allRows[0].text}, allRows[1:]
	}
	first, end := visibleWindow(allRows, m.cursor, capacity-len(pinned))

	screenLines := pinned
	for _, shown := range allRows[first:end] {
		if !shown.heading {
			m.hits.add(firstRow+len(screenLines), 2, inner, fmt.Sprintf("row:%d", shown.line))
		}
		screenLines = append(screenLines, shown.text)
	}
	if hidden := hiddenLines(allRows[end:]); hidden > 0 {
		screenLines = append(screenLines, dimStyle.Render(fmt.Sprintf("    … còn %d", hidden)))
	}
	return screenLines
}

// tableRows turns every section of the tab into rows: heading, column header, then the lines.
func (m manageModel) tableRows(tab manageTab, inner int) []tableRow {
	var built []tableRow
	sections := tab.layout()
	for n, section := range sections {
		sectionLines := tab.linesOf(sections, n)
		widths := tableWidths(tab, section.headers, sectionLines, inner)
		if len(tab.sections) > 0 {
			built = append(built, tableRow{text: headingText(section, sectionLines, inner), line: section.first, heading: true})
		}
		if hasEntries(sectionLines) {
			built = append(built, tableRow{text: columnHeader(section.headers, widths), line: section.first, heading: true})
		}
		for offset, line := range sectionLines {
			built = append(built, m.lineRows(tab, widths, line, section.first+offset, inner)...)
		}
	}
	return built
}

// lineRows is the row of a line, and the dim row under it when it has one.
func (m manageModel) lineRows(tab manageTab, widths []int, line manageLine, index, inner int) []tableRow {
	selected := index == m.cursor
	own := []tableRow{{text: lineText(tab, widths, line, selected, inner), line: index}}
	if line.sub != "" {
		own = append(own, tableRow{text: subText(line.sub, selected, inner), line: index})
	}
	return own
}

// visibleWindow picks the rows [first, end) to show in capacity rows: the selected line stays in
// view, and a line is never cut between its row and the dim row under it.
func visibleWindow(screenRows []tableRow, cursor, capacity int) (first, end int) {
	if len(screenRows) <= capacity {
		return 0, len(screenRows)
	}
	capacity-- // the last row says how many lines are below
	spanFirst, spanLast := cursorSpan(screenRows, cursor)
	first = min(max(spanLast+1-capacity, 0), spanFirst)
	end = min(first+capacity, len(screenRows))
	if end < len(screenRows) && !screenRows[end].heading && screenRows[end].line == screenRows[end-1].line {
		end--
	}
	return first, end
}

// cursorSpan is the first and last row of the selected line, with the heading above it.
func cursorSpan(screenRows []tableRow, cursor int) (first, last int) {
	first = -1
	for i, current := range screenRows {
		if current.line != cursor {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	return max(first, 0), last
}

// hiddenLines counts the lines that have a row among belowRows.
func hiddenLines(belowRows []tableRow) int {
	count, previous := 0, -1
	for _, below := range belowRows {
		if !below.heading && below.line != previous {
			count++
			previous = below.line
		}
	}
	return count
}

// hasEntries says the section has real lines, not only the invitation to add one.
func hasEntries(sectionLines []manageLine) bool {
	return len(sectionLines) > 0 && !sectionLines[0].placeholder
}

// tableWidths is the width of each column of a section. Columns are ordered by importance: on a
// narrow screen the last ones are dropped, then the widest ones are shortened.
func tableWidths(tab manageTab, headers []string, sectionLines []manageLine, inner int) []int {
	widths := make([]int, len(headers))
	for col, header := range headers {
		widths[col] = lipgloss.Width(header)
	}
	for _, line := range sectionLines {
		if line.placeholder {
			continue
		}
		for col, cell := range line.cells[:min(len(line.cells), len(widths))] {
			widths[col] = max(widths[col], lipgloss.Width(cell))
		}
		if tab.historyCol >= 0 {
			widths[tab.historyCol] = max(widths[tab.historyCol], len(line.history))
		}
	}
	if tab.statusCol >= 0 && tab.statusCol < len(widths) {
		widths[tab.statusCol] += 2 // the status is a badge with a space on each side
	}
	keep := len(widths)
	for keep > 2 && rowPrefix+columnGap*(keep-1)+keep*keptColumnWidth > inner {
		keep--
	}
	widths = widths[:keep]
	fitWidths(widths, inner-rowPrefix, columnGap)
	return widths
}

// fitWidths shrinks the widest columns until the table fits in total cells.
func fitWidths(widths []int, total, gap int) {
	for {
		used := gap * (len(widths) - 1)
		widest := 0
		for i, width := range widths {
			used += width
			if width > widths[widest] {
				widest = i
			}
		}
		excess := used - total
		if excess <= 0 || widths[widest] <= minColumn {
			return
		}
		widths[widest] -= min(excess, widths[widest]-minColumn)
	}
}

// headingText is the row that opens a section: its name, what it is for and how many entries it has.
func headingText(section tableSection, sectionLines []manageLine, inner int) string {
	count := len(sectionLines)
	if !hasEntries(sectionLines) {
		count = 0
	}
	title := keyStyle.Render(section.title)
	tail := dimStyle.Render(fmt.Sprintf("%d", count))
	hint := "  " + section.hint
	fill := inner - lipgloss.Width(title) - lipgloss.Width(hint) - lipgloss.Width(tail) - 2
	if fill < 1 {
		hint = ""
		fill = max(inner-lipgloss.Width(title)-lipgloss.Width(tail)-2, 1)
	}
	return title + dimStyle.Render(hint) + " " + borderStyle.Render(strings.Repeat("─", fill)) + " " + tail
}

// columnHeader is the row with the names of the columns.
func columnHeader(headers []string, widths []int) string {
	cells := make([]string, len(widths))
	for col, width := range widths {
		text := truncate(headers[col], width)
		cells[col] = text + strings.Repeat(" ", width-lipgloss.Width(text))
	}
	return headerStyle.Render(strings.Repeat(" ", rowPrefix) + strings.Join(cells, strings.Repeat(" ", columnGap)))
}

// lineText is the row of one line; the selected one is painted with a background across the table.
func lineText(tab manageTab, widths []int, line manageLine, selected bool, inner int) string {
	if line.placeholder {
		return placeholderText(line, selected, inner)
	}
	cells := make([]string, len(widths))
	for col, width := range widths {
		cells[col] = cellText(tab, line, col, width, selected)
	}
	if !selected {
		return "  " + severityStyle(line.severity).Render("●") + " " + strings.Join(cells, strings.Repeat(" ", columnGap))
	}
	text := selectedStyle.Render("› ") + selectedSeverityStyle(line.severity).Render("●") + selectedStyle.Render(" ") +
		strings.Join(cells, selectedStyle.Render(strings.Repeat(" ", columnGap)))
	return text + selectedStyle.Render(strings.Repeat(" ", max(inner-lipgloss.Width(text), 0)))
}

// cellText is one cell padded to the width of its column: plain text, the status badge or the history glyphs.
func cellText(tab manageTab, line manageLine, col, width int, selected bool) string {
	switch col {
	case tab.historyCol:
		return historyText(line.history, width, selected)
	case tab.statusCol:
		return statusText(line, col, width, selected)
	}
	text := ""
	if col < len(line.cells) {
		text = truncate(line.cells[col], width)
	}
	return paint(text+strings.Repeat(" ", max(width-lipgloss.Width(text), 0)), selected)
}

// statusText is the status as a coloured badge, e.g. " LỖI ".
func statusText(line manageLine, col, width int, selected bool) string {
	word := strings.TrimSpace(truncate(line.cells[col], max(width-2, 1)))
	return badge(line.severity, word) + paint(strings.Repeat(" ", max(width-lipgloss.Width(word)-2, 0)), selected)
}

// historyText is one glyph per recent run, oldest first: ✓ ok, ▲ warning, ✗ failed.
func historyText(history []string, width int, selected bool) string {
	glyphs := history[max(len(history)-width, 0):]
	var text strings.Builder
	for _, outcome := range glyphs {
		glyph, level := outcomeGlyph(outcome)
		style := severityStyle(level)
		if selected {
			style = selectedSeverityStyle(level)
		}
		text.WriteString(style.Render(glyph))
	}
	return text.String() + paint(strings.Repeat(" ", width-len(glyphs)), selected)
}

// outcomeGlyph is the one-cell symbol and colour of a run outcome in the history column.
func outcomeGlyph(outcome string) (string, severity) {
	switch outcome {
	case "failed":
		return "✗", sevBad
	case "warning":
		return "▲", sevWarn
	}
	return "✓", sevOK
}

// paint gives text the selected line's background when the line is selected.
func paint(text string, selected bool) string {
	if selected {
		return selectedStyle.Render(text)
	}
	return text
}

// subText is the dim row under a line: "└ the full error".
func subText(sub string, selected bool, inner int) string {
	text := strings.Repeat(" ", rowPrefix) + "└ " + truncate(sub, max(inner-rowPrefix-2, 1))
	if selected {
		return selectedDimStyle.Render(text + strings.Repeat(" ", max(inner-lipgloss.Width(text), 0)))
	}
	return dimStyle.Render(text)
}

// placeholderText is the row that invites adding the first entry of an empty section.
func placeholderText(line manageLine, selected bool, inner int) string {
	text := "+ " + truncate(line.cells[0], max(inner-4, 1))
	if selected {
		text = "› " + text
		return selectedStyle.Render(text + strings.Repeat(" ", max(inner-lipgloss.Width(text), 0)))
	}
	return "  " + dimStyle.Render(text)
}

func truncate(text string, width int) string {
	if lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}
