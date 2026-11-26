package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"sentinel/internal/backup"
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
	if m.mode == modeTable {
		return strings.Join(m.renderDashboard(), "\n")
	}
	m.hits.offset = 1 // the blank line under the frame's top border
	return m.frame(append([]string{""}, m.pinKeyBar(m.renderScreen())...))
}

// renderScreen is the lines of a screen other than the main one; each ends with its key bar.
func (m manageModel) renderScreen() []string {
	switch m.mode {
	case modeBusy:
		return m.renderBusy()
	case modeSnapshots:
		return m.renderSnapshots()
	case modeTarget:
		return m.renderTarget()
	case modeConfirm:
		return m.renderConfirm()
	case modeResult:
		return m.renderResult()
	case modeForm:
		return m.renderForm()
	case modeDelete:
		return m.renderDelete()
	case modeBrowse:
		return m.renderBrowse()
	case modeHelp:
		return m.renderHelp()
	}
	return nil
}

// frameCols and frameRows are what the frame around a screen takes: the border and one space on
// each side, and the top and bottom border lines. m.width and m.height are the room inside; the
// main screen has no frame and uses the whole terminal.
const (
	frameCols = 4
	frameRows = 2
)

// pinKeyBar moves the key bar, the last line of a screen, to the bottom of the frame under a rule,
// with the click targets of its keys, and pads the lines above to fill the room.
func (m manageModel) pinKeyBar(lines []string) []string {
	if len(lines) < 2 {
		return lines
	}
	bar := lines[len(lines)-1]
	barRow := m.hits.offset + len(lines) - 1
	var body []string
	for _, text := range lines[:len(lines)-1] {
		body = append(body, strings.Split(text, "\n")...)
	}
	for len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
	}
	for len(body) < m.height-m.hits.offset-2 {
		body = append(body, "")
	}
	m.hits.moveRow(barRow, m.hits.offset+len(body)+1)
	return append(body, borderStyle.Render(strings.Repeat("─", m.width)), bar)
}

// frame draws the rounded border around the screen's lines, with the title and the time of the
// last update in the top border. A line wider than the room is cut, so it cannot break the border.
// Until the terminal size is known the lines are returned as they are.
func (m manageModel) frame(lines []string) string {
	if m.width <= 0 {
		return strings.Join(lines, "\n")
	}
	line := borderStyle.Render
	out := []string{m.frameTop()}
	clip := lipgloss.NewStyle().MaxWidth(m.width)
	var inside []string
	for _, text := range lines {
		inside = append(inside, strings.Split(text, "\n")...)
	}
	for len(inside) < m.height {
		inside = append(inside, "")
	}
	for _, text := range inside {
		text = clip.Render(text)
		out = append(out, line("│ ")+text+strings.Repeat(" ", max(m.width-lipgloss.Width(text), 0))+line(" │"))
	}
	out = append(out, line("╰"+strings.Repeat("─", m.width+frameCols-2)+"╯"))
	return strings.Join(out, "\n")
}

// frameTop is the top border: the product and server on the left, the last update on the right.
func (m manageModel) frameTop() string {
	updated := "đang tải…"
	if m.loaded {
		updated = "cập nhật " + m.report.GeneratedAt.Local().Format("15:04:05")
	}
	left := lipglossStyleAccent.Render("◆ ") + titleStyle.Render("Sentinel") + dimStyle.Render("  "+m.report.Server)
	right := dimStyle.Render(updated)
	line := borderStyle.Render
	total := m.width + frameCols
	if room := total - 8 - lipgloss.Width(left) - lipgloss.Width(right); room >= 1 {
		return line("╭─ ") + left + line(" "+strings.Repeat("─", room)+" ") + right + line(" ─╮")
	}
	return line("╭─ ") + left + line(" "+strings.Repeat("─", max(total-5-lipgloss.Width(left), 0))+"╮")
}

// keyChip is one hint of the key bar: the key, what it does and the key a click on it presses.
type keyChip struct{ key, label, action string }

// keyChips reads key hints given as pairs ("b", "backup", "s", "snapshots", ...).
func keyChips(pairs []string) []keyChip {
	var chips []keyChip
	for i := 0; i+1 < len(pairs); i += 2 {
		action := pairs[i]
		switch action {
		case "↑↓", "1-5":
			action = "" // several keys: nothing to click
		case "←":
			action = "left"
		case "any key", "phím bất kỳ":
			action = "esc"
		}
		chips = append(chips, keyChip{key: pairs[i], label: pairs[i+1], action: action})
	}
	return chips
}

// keyBar renders key hints as a footer line; screenRow is the line's row within the current
// renderer's lines. When the screen is too narrow the labels are dropped and only the keys remain.
// Each hint is a click target that presses its key.
func (m manageModel) keyBar(screenRow int, pairs ...string) string {
	return m.keyLine(screenRow, m.width, "", pairs...)
}

// keyLine is keyBar for a line of total cells, with right text (the position in the list) at its end.
func (m manageModel) keyLine(screenRow, total int, right string, pairs ...string) string {
	chips := keyChips(pairs)
	room := total - 1
	if right != "" {
		room -= lipgloss.Width(right) + 2
	}
	mark := len(m.hits.safeTargets())
	text, width := m.keyChipsText(chips, true, screenRow)
	if width > room {
		m.hits.truncate(mark)
		text, width = m.keyChipsText(chips, false, screenRow)
	}
	if right == "" {
		return text
	}
	return text + strings.Repeat(" ", max(total-width-lipgloss.Width(right)-1, 1)) + dimStyle.Render(right)
}

// keyChipsText is the chips on one line, with or without their labels, and its width; it records
// a click target for each.
func (m manageModel) keyChipsText(chips []keyChip, withLabels bool, screenRow int) (text string, width int) {
	separator := " "
	if withLabels {
		separator = "  "
	}
	column := 1 // the line starts with one space
	var parts []string
	for _, chip := range chips {
		part, partWidth := barKeyStyle.Render(chip.key), lipgloss.Width(chip.key)
		if withLabels {
			part += " " + barDimStyle.Render(chip.label)
			partWidth += 1 + lipgloss.Width(chip.label)
		}
		parts = append(parts, part)
		m.hits.add(screenRow, column, partWidth, chip.action)
		column += partWidth + len(separator)
	}
	return " " + strings.Join(parts, separator), column - len(separator)
}

// wrap breaks text into lines of at most width cells (at least 20), on spaces.
func wrap(text string, width int) string {
	return strings.Join(wrapLines(text, max(width, 20)), "\n")
}

// renderBusy, renderSnapshots, renderTarget, renderConfirm and renderResult are the
// screens other than the three tabs.

func (m manageModel) renderBusy() []string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	elapsed := time.Since(m.busyStart).Round(time.Second)
	return []string{
		lipglossAccent(frames[m.busyFrame%len(frames)]) + " " + m.busyText + dimStyle.Render("  "+elapsed.String()),
		"",
		m.keyBar(2, "ctrl+c", "huỷ"),
	}
}

func (m manageModel) renderSnapshots() []string {
	screenLines := []string{titleStyle.Render(fmt.Sprintf("Snapshots của %s trong %s", m.snapshotJob, m.snapshotRepo)), ""}
	if len(m.snapshots) == 0 {
		return append(screenLines, dimStyle.Render("Chưa có snapshot nào của job này trong repository này."), "", m.keyBar(len(screenLines)+2, "esc", "quay lại"))
	}
	visible := max(m.height-8, 3)
	first := firstVisible(m.snapshotCursor, visible)
	screenLines = append(screenLines, "  "+headerStyle.Render(fmt.Sprintf("%-10s %-17s %s", "SNAPSHOT", "THỜI GIAN", "KÍCH THƯỚC")))
	for i := first; i < min(first+visible, len(m.snapshots)); i++ {
		snapshot := m.snapshots[i]
		size := ""
		if snapshot.Summary != nil {
			size = backup.HumanBytes(snapshot.Summary.TotalBytes)
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
		screenLines = append(screenLines, dimStyle.Render(fmt.Sprintf("  … còn %d", hidden)))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "↑↓", "chọn", "enter", "restore…", "esc", "quay lại"))
}

func (m manageModel) renderTarget() []string {
	screenLines := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s của %s", shortID(m.chosen.ID), m.snapshotJob)),
		"",
		"Thư mục để restore vào (đường dẫn tuyệt đối):",
		"  " + m.target + lipglossAccent("▏"),
	}
	if len(m.targetOptions) > 0 {
		screenLines = append(screenLines, "  "+dimStyle.Render(truncate(strings.Join(m.targetOptions, "  "), max(m.width-4, 10))))
	}
	if m.inputError != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(m.inputError))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "tab", "điền", "enter", "tiếp tục", "esc", "quay lại"))
}

func (m manageModel) renderConfirm() []string {
	screenLines := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s của %s", shortID(m.chosen.ID), m.snapshotJob)),
		"",
		wrap(fmt.Sprintf("Snapshot %s (%s) từ %s sẽ được restore vào %s.",
			shortID(m.chosen.ID), m.chosen.Time.Local().Format("2006-01-02 15:04"), m.snapshotRepo, m.target), m.width-2),
	}
	if m.target == "/" {
		screenLines = append(screenLines, "", severityStyle(sevWarn).Render("Đích là / : file được restore tại chỗ và ghi đè dữ liệu hiện có."))
	}
	screenLines = append(screenLines, "", "Gõ tên job ("+m.snapshotJob+") để xác nhận:", "  "+m.confirm+lipglossAccent("▏"))
	if m.inputError != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(m.inputError))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "enter", "restore", "esc", "huỷ"))
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
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "phím bất kỳ", "tiếp tục"))
}

func lipglossAccent(text string) string {
	return lipglossStyleAccent.Render(text)
}
