package main

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m manageModel) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m manageModel) render() string {
	if m.quitting {
		return ""
	}
	m.hits.reset()
	screenLines := m.renderHeader()
	m.hits.offset = len(screenLines)
	switch m.mode {
	case modeBusy:
		screenLines = append(screenLines, m.renderBusy()...)
	case modeSnapshots:
		screenLines = append(screenLines, m.renderSnapshots()...)
	case modeTarget:
		screenLines = append(screenLines, m.renderTarget()...)
	case modeConfirm:
		screenLines = append(screenLines, m.renderConfirm()...)
	case modeResult:
		screenLines = append(screenLines, m.renderResult()...)
	case modeForm:
		screenLines = append(screenLines, m.renderForm()...)
	case modeDelete:
		screenLines = append(screenLines, m.renderDelete()...)
	case modeBrowse:
		screenLines = append(screenLines, m.renderBrowse()...)
	case modeHelp:
		screenLines = append(screenLines, m.renderHelp()...)
	default:
		screenLines = append(screenLines, m.renderTabs()...)
	}
	return m.frame(screenLines)
}

func (m manageModel) renderHeader() []string {
	if m.mode != modeTable || !m.loaded {
		return []string{""}
	}
	if m.showCards() {
		return append(m.renderCards(), "")
	}
	return []string{m.summary(), ""}
}

// frameCols and frameRows are what the frame around the screen takes: the border and one
// space on each side, and the top and bottom border lines. m.width and m.height are the room inside.
const (
	frameCols = 4
	frameRows = 2
)

// frame draws the rounded border around the screen's lines, with the title and the time of the
// last update in the top border. Until the terminal size is known the lines are returned as they are.
func (m manageModel) frame(lines []string) string {
	if m.width <= 0 {
		return strings.Join(lines, "\n")
	}
	updated := "loading…"
	if m.loaded {
		updated = "updated " + m.report.GeneratedAt.Local().Format("15:04:05")
	}
	left := lipglossStyleAccent.Render("◆ ") + titleStyle.Render("Sentinel") + dimStyle.Render("  "+m.report.Server)
	right := dimStyle.Render(updated)
	total := m.width + frameCols
	line := borderStyle.Render
	room := total - 8 - lipgloss.Width(left) - lipgloss.Width(right)
	var top string
	if room >= 1 {
		top = line("╭─ ") + left + line(" "+strings.Repeat("─", room)+" ") + right + line(" ─╮")
	} else {
		top = line("╭─ ") + left + line(" "+strings.Repeat("─", max(total-5-lipgloss.Width(left), 0))+"╮")
	}
	out := []string{top}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	for _, text := range lines {
		out = append(out, line("│ ")+text+strings.Repeat(" ", max(m.width-lipgloss.Width(text), 0))+line(" │"))
	}
	out = append(out, line("╰"+strings.Repeat("─", total-2)+"╯"))
	return strings.Join(out, "\n")
}

func (m manageModel) renderTabs() []string {
	tabs := m.tabs()
	var screenLines []string
	var tabBar []string
	column := 0
	for i, label := range m.tabLabels() {
		label = " " + label + " " // the active tab is a filled pill, so every tab has room for it
		m.hits.add(0, column, lipgloss.Width(label), fmt.Sprintf("tab:%d", i))
		column += lipgloss.Width(label) + lipgloss.Width(m.tabGap())
		switch {
		case i == m.tab:
			tabBar = append(tabBar, activeTab.Render(label))
		case (i == 2 && len(tabs[i].lines) > 0) || tabs[i].alarm:
			tabBar = append(tabBar, severityStyle(sevBad).Render(label))
		default:
			tabBar = append(tabBar, dimStyle.Render(label))
		}
	}
	rule := borderStyle.Render(strings.Repeat("─", m.width))
	screenLines = append(screenLines, strings.Join(tabBar, m.tabGap()), rule)

	if m.loadErr != nil {
		screenLines = append(screenLines, severityStyle(sevBad).Render("could not read the store: "+m.loadErr.Error()), "")
	}

	tab := tabs[m.tab]
	// Lines available for the table: the screen minus title, summary, tabs, rules,
	// column header, detail, notice and footer.
	screenLines = append(screenLines, m.renderTable(tab, m.visibleRows(), len(screenLines))...)

	screenLines = append(screenLines, rule)
	if len(tab.lines) > 0 {
		screenLines = append(screenLines, wrap(tab.lines[m.cursor].detail, m.width-2))
	} else {
		screenLines = append(screenLines, dimStyle.Render(m.emptyText(m.tab)))
	}
	if m.notice != "" {
		style := severityStyle(sevOK)
		if m.noticeBad {
			style = severityStyle(sevBad)
		}
		screenLines = append(screenLines, style.Render(wrap(m.notice, m.width-2)))
	}
	footerKeys := []string{"tab", "switch", "↑↓", "select", "r", "refresh", "?", "help", "q", "quit"}
	switch m.tab {
	case doctorTab:
		footerKeys = []string{"tab", "switch", "↑↓", "select", "r", "run checks", "?", "help", "q", "quit"}
	case configTab:
		footerKeys = []string{"a", "add", "e", "edit", "d", "remove", "tab", "switch", "?", "help", "q", "quit"}
	case 0:
		footerKeys = []string{"b", "backup", "s", "snapshots", "a", "add", "e", "edit", "d", "remove", "?", "help", "q", "quit"}
	}
	for m.hits.offset+len(screenLines)+2 < m.height { // the footer sits on the last line of the frame
		screenLines = append(screenLines, "")
	}
	footer := m.keyBar(len(screenLines)+1, footerKeys...)
	return append(screenLines, borderStyle.Render(strings.Repeat("─", m.width)), footer)
}

// tabLabels are the tab bar's labels, e.g. "1 Backups 3"; the tab bar joins them with tabGap().
// On a narrow screen the counts go first, then the names of the tabs that are not open.
func (m manageModel) tabLabels() []string {
	tabs := m.tabs()
	build := func(counts, names bool) []string {
		var labels []string
		for i, tab := range tabs {
			label := fmt.Sprintf("%d", i+1)
			if names || i == m.tab {
				label += " " + tab.title
			}
			if count := len(tab.lines); counts && count > 0 && i != configTab {
				label += fmt.Sprintf(" %d", count)
			}
			labels = append(labels, label)
		}
		return labels
	}
	fits := func(labels []string) bool {
		total := lipgloss.Width(m.tabGap()) * (len(labels) - 1)
		for _, label := range labels {
			total += lipgloss.Width(label) + 2 // the pill's padding
		}
		return total <= m.width
	}
	for _, attempt := range [][2]bool{{true, true}, {false, true}, {false, false}} {
		if labels := build(attempt[0], attempt[1]); fits(labels) || attempt == [2]bool{false, false} {
			return labels
		}
	}
	return nil
}

// tabGap separates the tab labels; a narrow screen packs them closer.
func (m manageModel) tabGap() string {
	if m.width < 60 {
		return ""
	}
	return " "
}

// visibleRows is how many table lines fit: the screen minus title, summary, tabs,
// rules, column header, detail, notice and footer.
func (m manageModel) visibleRows() int {
	rows := m.height - 12
	if m.showCards() {
		rows -= 2 // the cards are three lines instead of one
	}
	return max(rows, 3)
}

func (m manageModel) emptyText(tab int) string {
	switch tab {
	case doctorTab:
		return m.doctorWaitText()
	case configTab:
		return "The config could not be read."
	}
	return [...]string{
		"No backups recorded yet. Run `sentinel backup run <job>` or start the daemon.",
		"No uptime checks recorded yet.",
		"No open alerts.",
	}[tab]
}

// renderTable writes the table; base is the row of its header line within the screen's lines.
func (m manageModel) renderTable(tab manageTab, visible, base int) []string {
	if len(tab.lines) == 0 {
		return nil
	}
	widths := make([]int, len(tab.headers))
	for i, header := range tab.headers {
		widths[i] = lipgloss.Width(header)
	}
	for _, line := range tab.lines {
		for i, cell := range line.cells {
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
		if tab.historyCol >= 0 {
			widths[tab.historyCol] = max(widths[tab.historyCol], len(line.history))
		}
	}
	if tab.statusCol >= 0 && tab.statusCol < len(widths) {
		widths[tab.statusCol] += 2 // the status is a badge with a space on each side
	}
	const gap = 2
	const prefix = 4 // the selection marker and the status dot
	// Columns are ordered by importance: on a narrow screen the last ones are dropped.
	keep := len(widths)
	for keep > 2 && prefix+gap*(keep-1)+keep*minColumn > m.width {
		keep--
	}
	widths = widths[:keep]
	fitWidths(widths, m.width-prefix, gap)

	// cell renders one cell padded to its column; on the selected line every piece carries the background.
	cell := func(col int, line manageLine, selected bool) string {
		plainStyle := func(text string) string {
			if selected {
				return selectedStyle.Render(text)
			}
			return text
		}
		levelStyle := func(level severity) lipgloss.Style {
			if selected {
				return selectedSeverityStyle(level)
			}
			return severityStyle(level)
		}
		if col == tab.historyCol {
			glyphs := line.history[max(len(line.history)-widths[col], 0):]
			var text strings.Builder
			for _, outcome := range glyphs {
				glyph, level := outcomeGlyph(outcome)
				text.WriteString(levelStyle(level).Render(glyph))
			}
			return text.String() + plainStyle(strings.Repeat(" ", widths[col]-len(glyphs)))
		}
		text := truncate(line.cells[col], widths[col])
		text += strings.Repeat(" ", max(widths[col]-lipgloss.Width(text), 0))
		if col == tab.statusCol {
			word := strings.TrimSpace(truncate(line.cells[col], max(widths[col]-2, 1)))
			return badge(line.severity, word) + plainStyle(strings.Repeat(" ", max(widths[col]-lipgloss.Width(word)-2, 0)))
		}
		return plainStyle(text)
	}
	joined := func(line manageLine, selected bool) string {
		parts := make([]string, keep)
		for col := range parts {
			parts[col] = cell(col, line, selected)
		}
		gapText := strings.Repeat(" ", gap)
		if selected {
			gapText = selectedStyle.Render(gapText)
		}
		return strings.Join(parts, gapText)
	}

	header := make([]string, keep)
	for col := range header {
		text := truncate(tab.headers[col], widths[col])
		header[col] = text + strings.Repeat(" ", max(widths[col]-lipgloss.Width(text), 0))
	}
	headerText := strings.Repeat(" ", prefix) + strings.Join(header, strings.Repeat(" ", gap))
	screenLines := []string{headerBand.Render(headerText + strings.Repeat(" ", max(m.width-lipgloss.Width(headerText), 0)))}
	first := firstVisible(m.cursor, visible)
	for i := first; i < min(first+visible, len(tab.lines)); i++ {
		line := tab.lines[i]
		m.hits.add(base+len(screenLines), 0, m.width, fmt.Sprintf("row:%d", i))
		if i == m.cursor {
			text := selectedStyle.Render("› ") + selectedSeverityStyle(line.severity).Render("●") + selectedStyle.Render(" ") + joined(line, true)
			screenLines = append(screenLines, text+selectedStyle.Render(strings.Repeat(" ", max(m.width-lipgloss.Width(text), 0))))
			continue
		}
		screenLines = append(screenLines, "  "+severityStyle(line.severity).Render("●")+" "+joined(line, false))
	}
	if hidden := len(tab.lines) - (first + visible); hidden > 0 {
		screenLines = append(screenLines, dimStyle.Render(fmt.Sprintf("    … %d more", hidden)))
	}
	return screenLines
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

// keyBar renders key hints ("b", "backup", "s", "snapshots", ...) as a footer line; row is the
// line's row within the current renderer's lines. When the screen is too narrow the labels are
// dropped and only the keys remain. Each hint is a click target that presses its key.
func (m manageModel) keyBar(screenRow int, pairs ...string) string {
	type chip struct{ key, label, action string }
	var chips []chip
	for i := 0; i+1 < len(pairs); i += 2 {
		action := pairs[i]
		switch action {
		case "↑↓":
			action = "" // two keys: nothing to click
		case "←":
			action = "left"
		case "any key":
			action = "esc"
		}
		chips = append(chips, chip{pairs[i], pairs[i+1], action})
	}
	build := func(withLabels bool) (string, int) {
		separator := " "
		if withLabels {
			separator = "  "
		}
		var parts []string
		column := 1 // the band starts with one space
		for _, chip := range chips {
			text, width := barKeyStyle.Render(chip.key), lipgloss.Width(chip.key)
			if withLabels {
				text += barStyle.Render(" ") + barDimStyle.Render(chip.label)
				width += 1 + lipgloss.Width(chip.label)
			}
			parts = append(parts, text)
			if m.hits != nil && width > 0 {
				m.hits.add(screenRow, column, width, chip.action)
			}
			column += width + len(separator)
		}
		return barStyle.Render(" ") + strings.Join(parts, barStyle.Render(separator)), column - len(separator)
	}
	mark := len(m.hits.safeTargets())
	bar, width := build(true)
	if width > m.width {
		m.hits.truncate(mark)
		bar, width = build(false)
	}
	return bar + barStyle.Render(strings.Repeat(" ", max(m.width-width, 0))) // the band spans the screen
}

// fitWidths shrinks the widest columns until the table fits in total cells.
func fitWidths(widths []int, total, gap int) {
	const minimum = minColumn
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
		if excess <= 0 || widths[widest] <= minimum {
			return
		}
		widths[widest] -= min(excess, widths[widest]-minimum)
	}
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

// wrap breaks text into lines of at most width cells, on spaces.
func wrap(text string, width int) string {
	if width < 20 {
		width = 20
	}
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		if current != "" && lipgloss.Width(current)+1+lipgloss.Width(word) > width {
			lines = append(lines, current)
			current = word
			continue
		}
		current = strings.TrimSpace(current + " " + word)
	}
	return strings.Join(append(lines, current), "\n")
}

// renderBusy, renderSnapshots, renderTarget, renderConfirm and renderResult are the
// screens other than the three tabs.

func (m manageModel) renderBusy() []string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	elapsed := time.Since(m.busyStart).Round(time.Second)
	return []string{
		lipglossAccent(frames[m.busyFrame%len(frames)]) + " " + m.busyText + dimStyle.Render("  "+elapsed.String()),
		"",
		m.keyBar(2, "ctrl+c", "cancel"),
	}
}

func (m manageModel) renderSnapshots() []string {
	screenLines := []string{titleStyle.Render(fmt.Sprintf("Snapshots of %s in %s", m.snapshotJob, m.snapshotRepo)), ""}
	if len(m.snapshots) == 0 {
		return append(screenLines, dimStyle.Render("No snapshots for this job in this repository yet."), "", m.keyBar(len(screenLines)+2, "esc", "back"))
	}
	visible := max(m.height-8, 3)
	first := firstVisible(m.snapshotCursor, visible)
	screenLines = append(screenLines, "  "+headerStyle.Render(fmt.Sprintf("%-10s %-17s %s", "SNAPSHOT", "TIME", "SIZE")))
	for i := first; i < min(first+visible, len(m.snapshots)); i++ {
		snapshot := m.snapshots[i]
		size := ""
		if snapshot.Summary != nil {
			size = humanBytes(snapshot.Summary.TotalBytes)
		}
		line := fmt.Sprintf("%-10s %-17s %s", shortID(snapshot.ID), snapshot.Time.Local().Format("2006-01-02 15:04"), size)
		m.hits.add(len(screenLines), 0, m.width, fmt.Sprintf("snap:%d", i))
		if i == m.snapshotCursor {
			text := "› " + line
			screenLines = append(screenLines, selectedStyle.Render(text+strings.Repeat(" ", max(m.width-lipgloss.Width(text), 0))))
		} else {
			screenLines = append(screenLines, "  "+line)
		}
	}
	if hidden := len(m.snapshots) - (first + visible); hidden > 0 {
		screenLines = append(screenLines, dimStyle.Render(fmt.Sprintf("  … %d more", hidden)))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "↑↓", "select", "enter", "restore…", "esc", "back"))
}

func (m manageModel) renderTarget() []string {
	screenLines := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s of %s", shortID(m.chosen.ID), m.snapshotJob)),
		"",
		"Directory to restore into (absolute path):",
		"  " + m.target + lipglossAccent("▏"),
	}
	if len(m.targetOptions) > 0 {
		screenLines = append(screenLines, "  "+dimStyle.Render(truncate(strings.Join(m.targetOptions, "  "), max(m.width-4, 10))))
	}
	if m.inputError != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(m.inputError))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "tab", "complete", "enter", "continue", "esc", "back"))
}

func (m manageModel) renderConfirm() []string {
	screenLines := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s of %s", shortID(m.chosen.ID), m.snapshotJob)),
		"",
		wrap(fmt.Sprintf("Snapshot %s (%s) from %s will be restored into %s.",
			shortID(m.chosen.ID), m.chosen.Time.Local().Format("2006-01-02 15:04"), m.snapshotRepo, m.target), m.width-2),
	}
	if m.target == "/" {
		screenLines = append(screenLines, "", severityStyle(sevWarn).Render("Target is / : files are restored in place and overwrite what is there."))
	}
	screenLines = append(screenLines, "", "Type the job name ("+m.snapshotJob+") to confirm:", "  "+m.confirm+lipglossAccent("▏"))
	if m.inputError != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(m.inputError))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "enter", "restore", "esc", "cancel"))
}

func (m manageModel) renderResult() []string {
	style := severityStyle(sevOK)
	if m.resultBad {
		style = severityStyle(sevBad)
	}
	first, rest, _ := strings.Cut(m.resultText, "\n")
	screenLines := []string{style.Render(wrap(first, m.width-2))}
	if rest != "" {
		screenLines = append(screenLines, rest)
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "any key", "continue"))
}

func lipglossAccent(text string) string {
	return lipglossStyleAccent.Render(text)
}
