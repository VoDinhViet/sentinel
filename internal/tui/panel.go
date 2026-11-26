package tui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// panel draws a rounded box of exactly width x height cells. title (already styled, may be
// empty) sits in the top border. Every body line is clipped and padded to the inside of the
// box, so a long value can never push the border out of line.
func panel(title string, width, height int, border color.Color, body []string) []string {
	if width < 5 || height < 2 {
		return nil
	}
	edge := lipgloss.NewStyle().Foreground(border).Render
	inner := width - 4
	clip := lipgloss.NewStyle().MaxWidth(inner)

	top := edge("╭" + strings.Repeat("─", width-2) + "╮")
	if room := width - 5 - lipgloss.Width(title); title != "" && room >= 0 {
		top = edge("╭─ ") + title + edge(" "+strings.Repeat("─", room)+"╮")
	}
	lines := []string{top}
	var bodyLines []string
	for _, text := range body {
		bodyLines = append(bodyLines, strings.Split(text, "\n")...)
	}
	for i := range height - 2 {
		text := ""
		if i < len(bodyLines) {
			text = clip.Render(bodyLines[i])
		}
		lines = append(lines, edge("│")+" "+text+strings.Repeat(" ", max(inner-lipgloss.Width(text), 0))+" "+edge("│"))
	}
	return append(lines, edge("╰"+strings.Repeat("─", width-2)+"╯"))
}

// joinColumns puts panels of the same height side by side, gap cells apart.
func joinColumns(panels [][]string, gap int) []string {
	if len(panels) == 0 {
		return nil
	}
	space := strings.Repeat(" ", gap)
	lines := make([]string, len(panels[0]))
	for i := range lines {
		parts := make([]string, len(panels))
		for j, column := range panels {
			parts[j] = column[i]
		}
		lines[i] = strings.Join(parts, space)
	}
	return lines
}

// progressBar is a bar of width cells filled to percent, in the colour of level.
func progressBar(percent float64, width int, level severity) string {
	filled := min(max(int(math.Round(percent/100*float64(width))), 0), width)
	return severityStyle(level).Render(strings.Repeat("█", filled)) + dimStyle.Render(strings.Repeat("░", width-filled))
}

// sparkBlocks are the eight heights of a sparkline, lowest first.
var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// sparkline draws the last width values as bars scaled to the largest one; a value whose
// failed flag is set is drawn as a red cross instead.
func sparkline(values []int64, failed []bool, width int) string {
	if len(values) > width {
		values, failed = values[len(values)-width:], failed[max(len(failed)-width, 0):]
	}
	var peak int64
	for i, value := range values {
		if failedAt(failed, i) {
			continue
		}
		peak = max(peak, value)
	}
	var line strings.Builder
	for i, value := range values {
		if failedAt(failed, i) {
			line.WriteString(severityStyle(sevBad).Render("✗"))
			continue
		}
		level := 0
		if peak > 0 {
			level = int(value * int64(len(sparkBlocks)-1) / peak)
		}
		line.WriteString(severityStyle(sevOK).Render(string(sparkBlocks[min(level, len(sparkBlocks)-1)])))
	}
	return line.String()
}

// failedAt says value i of a sparkline is a failed run; a missing flag means it succeeded.
func failedAt(failed []bool, i int) bool {
	return i < len(failed) && failed[i]
}

// kvColumns lays label/value pairs out in as many columns as needed for maxRows rows, within width
// cells. A column is as wide as its own labels and values; when they do not all fit, the widest
// values are cut with an ellipsis.
func kvColumns(pairs [][2]string, width, maxRows int) []string {
	if len(pairs) == 0 {
		return nil
	}
	const gap, minColumnWidth = 3, 20
	columns := min((len(pairs)+maxRows-1)/maxRows, max((width+gap)/(minColumnWidth+gap), 1))
	rows := (len(pairs) + columns - 1) / columns
	columns = (len(pairs) + rows - 1) / rows // the last column is never empty

	labelWidths, valueWidths := make([]int, columns), make([]int, columns)
	taken := gap * (columns - 1)
	for column := range columns {
		for _, pair := range pairs[column*rows : min((column+1)*rows, len(pairs))] {
			labelWidths[column] = max(labelWidths[column], lipgloss.Width(pair[0]))
			valueWidths[column] = max(valueWidths[column], lipgloss.Width(pair[1]))
		}
		taken += labelWidths[column] + 1
	}
	fitWidths(valueWidths, width-taken, 0)

	lines := make([]string, rows)
	for i, pair := range pairs {
		column, row := i/rows, i%rows
		value := truncate(pair[1], valueWidths[column])
		cell := dimStyle.Render(pair[0]) + strings.Repeat(" ", labelWidths[column]-lipgloss.Width(pair[0])+1)
		cell += value + strings.Repeat(" ", max(valueWidths[column]-lipgloss.Width(value), 0))
		if column > 0 {
			cell = strings.Repeat(" ", gap) + cell
		}
		lines[row] += cell
	}
	return lines
}

// wrapLines breaks text into lines of at most width cells, on spaces; a word wider than a
// line is cut, so a long path never overflows.
func wrapLines(text string, width int) []string {
	width = max(width, 1)
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		for lipgloss.Width(word) > width {
			if current != "" {
				lines, current = append(lines, current), ""
			}
			head, rest := cutWidth(word, width)
			lines, word = append(lines, head), rest
		}
		switch {
		case current == "":
			current = word
		case lipgloss.Width(current)+1+lipgloss.Width(word) > width:
			lines, current = append(lines, current), word
		default:
			current += " " + word
		}
	}
	return append(lines, current)
}

// cutWidth splits text after the longest prefix of at most width cells (at least one rune).
func cutWidth(text string, width int) (head, rest string) {
	runes := []rune(text)
	used, count := 0, 0
	for count < len(runes) && used+lipgloss.Width(string(runes[count])) <= width {
		used += lipgloss.Width(string(runes[count]))
		count++
	}
	count = max(count, 1)
	return string(runes[:count]), string(runes[count:])
}
