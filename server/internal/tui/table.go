package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// columnGap is the space between two columns, and rowMargin the two cells in front of every row for the
// selection marker.
const (
	columnGap = 2
	rowMargin = 2
	minColumn = 6
)

// table is what a screen shows: a header and lines, one of them selected. The column statusCol (-1 for
// none) is painted with the level of its line.
type table struct {
	title     string
	headers   []string
	statusCol int
	lines     []tableLine
	empty     string // what to say when there is no line
}

// tableLine is one row. cells are cut to fit the screen, so detail holds the full text of the line.
type tableLine struct {
	cells  []string
	level  level
	detail string
}

// troubled is how many lines are in trouble (bad or warn).
func (t table) troubled() int {
	count := 0
	for _, line := range t.lines {
		if line.level == levelBad || line.level == levelWarn {
			count++
		}
	}
	return count
}

// render draws the header and as many lines as fit in capacity rows, the selected one in view.
func (t table) render(cursor, width, capacity int) []string {
	if len(t.lines) == 0 {
		return []string{dimStyle.Render(truncate(t.empty, width))}
	}
	widths := t.widths(max(width-rowMargin, 1))
	out := []string{"  " + headerStyle.Render(joinCells(t.headers, widths))}
	first, end := window(len(t.lines), cursor, max(capacity-1, 2))
	for index := first; index < end; index++ {
		out = append(out, t.row(index, widths, index == cursor))
	}
	if hidden := len(t.lines) - end; hidden > 0 {
		out = append(out, dimStyle.Render(fmt.Sprintf("  … còn %d dòng nữa", hidden)))
	}
	return out
}

// window picks the lines [first, end) to show in room rows so that the cursor is in view. While lines
// are left below, the last row is kept for the note that says how many.
func window(count, cursor, room int) (first, end int) {
	if count <= room {
		return 0, count
	}
	first = max(cursor+1-(room-1), 0)
	end = first + room - 1
	if end >= count {
		first, end = count-room, count
	}
	return first, end
}

// widths gives every column the room its longest text needs, so that the whole row fits in inner cells:
// the widest column gives up room first, and when every column is down to minColumn the last ones are
// dropped (the result then has fewer columns than the table). What is left over goes to the last column.
func (t table) widths(inner int) []int {
	widths := make([]int, len(t.headers))
	for column, header := range t.headers {
		widths[column] = lipgloss.Width(header)
		for _, line := range t.lines {
			widths[column] = max(widths[column], lipgloss.Width(line.cells[column]))
		}
	}
	for total(widths) > inner {
		widest := 0
		for column, width := range widths {
			if width > widths[widest] {
				widest = column
			}
		}
		switch {
		case widths[widest] > minColumn:
			widths[widest]--
		case len(widths) > 1:
			widths = widths[:len(widths)-1]
		default:
			widths[0] = max(inner, 1)
		}
	}
	widths[len(widths)-1] += inner - total(widths)
	return widths
}

// total is the room a row with these column widths takes.
func total(widths []int) int {
	sum := columnGap * (len(widths) - 1)
	for _, width := range widths {
		sum += width
	}
	return sum
}

func (t table) row(index int, widths []int, selected bool) string {
	line := t.lines[index]
	plain := make([]string, len(widths)) // a narrow screen drops the last columns
	for column, width := range widths {
		plain[column] = pad(truncate(line.cells[column], width), width)
	}
	if !selected {
		if t.statusCol >= 0 && t.statusCol < len(plain) && line.level != levelNone { // a narrow screen may have dropped the column
			plain[t.statusCol] = levelStyle(line.level).Render(plain[t.statusCol])
		}
		return "  " + strings.Join(plain, strings.Repeat(" ", columnGap))
	}
	// The selected row is one background from edge to edge, so each cell is painted on it.
	var painted []string
	for column, cell := range plain {
		style := selectedStyle
		if column == t.statusCol && line.level != levelNone {
			style = levelStyle(line.level).Background(colourSelected)
		}
		painted = append(painted, style.Render(cell))
	}
	return selectedStyle.Render("› ") + strings.Join(painted, selectedStyle.Render(strings.Repeat(" ", columnGap)))
}

func joinCells(cells []string, widths []int) string {
	padded := make([]string, len(widths))
	for column, width := range widths {
		padded[column] = pad(truncate(cells[column], width), width)
	}
	return strings.Join(padded, strings.Repeat(" ", columnGap))
}

// pad adds spaces to text until it is width cells wide.
func pad(text string, width int) string {
	return text + strings.Repeat(" ", max(width-lipgloss.Width(text), 0))
}

// truncate shortens text to width cells with a "…" at the end, in one pass over the runes.
func truncate(text string, width int) string {
	if lipgloss.Width(text) <= width {
		return text
	}
	used, kept := 0, 0
	for index, char := range text {
		used += lipgloss.Width(string(char))
		if used+1 > width {
			break
		}
		kept = index + len(string(char))
	}
	return text[:kept] + "…"
}
