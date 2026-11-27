package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"sentinel/server/internal/format"
)

func (m model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	return view
}

// tabTable is the table of the tab on show.
func (m model) tabTable() table {
	if m.tab == alertsTab {
		return alertsOfControlPlane(m.alerts, m.now(), m.loaded)
	}
	return serversTable(m.servers, m.now(), m.loaded)
}

// render is the whole screen: the lines are padded to the height so the key bar sits at the bottom.
func (m model) render() string {
	var body []string
	var keys string
	switch {
	case m.help:
		body, keys = helpLines(), keyBar(m.width, "bấm phím bất kỳ", "đóng")
	case m.opened:
		body, keys = m.serverLines(), keyBar(m.width, "tab", "đổi mục", "↑↓", "chọn", "r", "hỏi lại", "?", "trợ giúp", "esc", "quay lại")
	default:
		body, keys = m.tabLines(), keyBar(m.width, "tab", "đổi tab", "↑↓", "chọn", "enter", "xem server", "r", "hỏi lại", "?", "trợ giúp", "q", "thoát")
	}
	for len(body) < m.height-2 {
		body = append(body, "")
	}
	body = body[:max(min(len(body), m.height-2), 0)]
	cut := lipgloss.NewStyle().MaxWidth(max(m.width, 1)) // whatever the lines above did not fit, so that no line wraps
	for index, line := range body {
		body[index] = cut.Render(line)
	}
	keys = cut.Render(keys)
	switch {
	case m.height < 1:
		return ""
	case m.height < 3: // no room for the rule: the key bar alone
		return keys
	}
	return strings.Join(append(body, ruleStyle.Render(strings.Repeat("─", max(m.width, 1))), keys), "\n")
}

// header is the name, then a status if there is one, and how fresh the data is at the right.
func (m model) header(name, status string, statusOf level) string {
	right := "chưa có dữ liệu"
	if m.loaded {
		right = "cập nhật " + m.loadedAt.Local().Format("15:04:05")
	}
	rightText := dimStyle.Render(right)
	if m.loadErr != nil {
		rightText = levelStyle(levelBad).Render("không hỏi được control plane")
	}
	statusText := ""
	if status != "" {
		statusText = "  " + levelStyle(statusOf).Render(truncate(status, 24))
	}
	room := max(m.width-lipgloss.Width(rightText)-lipgloss.Width(statusText)-3, 4) // the margin, and one space before the right text
	left := titleStyle.Render(truncate(name, room)) + statusText
	return " " + left + strings.Repeat(" ", max(m.width-lipgloss.Width(left)-lipgloss.Width(rightText)-2, 1)) + rightText
}

func (m model) tabLines() []string {
	troubledServers := 0
	for _, server := range m.servers {
		if server.Status == "OFFLINE" {
			troubledServers++
		}
	}
	tabs := []string{
		tabPill(m.tab == serversTab, 1, "Server", troubledServers),
		tabPill(m.tab == alertsTab, 2, "Alert", len(m.alerts)),
	}
	current := m.tabTable()
	lines := []string{m.header("sentinelctl", "", levelNone), " " + strings.Join(tabs, " "), ""}
	lines = append(lines, current.render(m.cursor, m.width, max(m.height-len(lines)-tableMargin, 3))...)
	lines = append(lines, "")
	lines = append(lines, m.detailLines(current, m.cursor)...)
	if m.loadErr != nil {
		lines = append(lines, levelStyle(levelBad).Render(" "+truncate(m.loadErr.Error(), m.width-2)))
	}
	return lines
}

// detailLines is the full text of the selected line, under the table: up to detailRows lines.
func (m model) detailLines(current table, cursor int) []string {
	if len(current.lines) == 0 {
		return nil
	}
	var lines []string
	for _, text := range wrap(current.lines[min(cursor, len(current.lines)-1)].detail, max(m.width-2, 10), detailRows) {
		lines = append(lines, " "+dimStyle.Render(text))
	}
	return lines
}

// tabPill is "1 Server" with a count of what is in trouble; the open tab is filled.
func tabPill(open bool, number int, title string, troubled int) string {
	label := fmt.Sprintf(" %d %s ", number, title)
	if troubled > 0 {
		label = fmt.Sprintf(" %d %s %d ", number, title, troubled)
	}
	switch {
	case open:
		return activeTabStyle.Render(label)
	case troubled > 0:
		return levelStyle(levelBad).Render(label)
	}
	return dimStyle.Render(label)
}

func (m model) serverLines() []string {
	now := m.now()
	screen := m.open
	lines := []string{m.header(m.open.name, format.Status(m.open.detail.Status, m.open.detail.State), statusLevel(m.open.detail.Status)), " " + dimStyle.Render(m.serverInfo(now)), ""}
	if screen.err != nil {
		lines = append(lines, levelStyle(levelBad).Render(" "+truncate("Không đọc được server này: "+screen.err.Error(), m.width-2)), "")
	}
	parts := screen.parts(now)
	var pills []string
	for index, part := range parts {
		pills = append(pills, tabPill(index == screen.current, index+1, part.title, part.troubled()))
	}
	lines = append(lines, " "+strings.Join(pills, " "), "")
	current := parts[screen.current]
	lines = append(lines, current.render(screen.cursor, m.width, max(m.height-len(lines)-tableMargin, 3))...)
	return append(append(lines, ""), m.detailLines(current, screen.cursor)...)
}

// serverInfo is what the control plane knows of the server besides its report.
func (m model) serverInfo(now time.Time) string {
	detail := m.open.detail
	info := fmt.Sprintf("thấy %s · phiên bản %s · chứng chỉ hết hạn %s", format.Ago(detail.LastSeen, now), format.Dash(detail.Version), format.Day(detail.CertNotAfter))
	if detail.ReportAt != nil {
		info += " · báo cáo " + format.Ago(detail.ReportAt, now)
	}
	return truncate(info, m.width-2)
}

// keyBar is the keys and what they do, as many as fit in width.
func keyBar(width int, pairs ...string) string {
	line, used := " ", 1
	for index := 0; index+1 < len(pairs); index += 2 {
		chip := keyStyle.Render(pairs[index]) + " " + dimStyle.Render(pairs[index+1])
		room := lipgloss.Width(chip) + 2
		if used+room > width && index > 0 {
			break
		}
		line += chip + "  "
		used += room
	}
	return strings.TrimRight(line, " ")
}

func helpLines() []string {
	keys := [][2]string{
		{"tab  1 2", "chuyển giữa Server và Alert (trong một server: Máy, Backup, Uptime, Alert)"},
		{"↑ ↓  j k", "chọn một dòng"},
		{"g  G", "về dòng đầu, dòng cuối"},
		{"enter", "xem một server: máy, backup, uptime check và cảnh báo của chính nó"},
		{"r", "hỏi lại control plane ngay (màn hình tự làm vậy mỗi 5 giây)"},
		{"esc  q", "quay lại; ở màn hình chính thì thoát"},
		{"ctrl+c", "thoát"},
	}
	lines := []string{" " + titleStyle.Render("Phím"), ""}
	for _, pair := range keys {
		lines = append(lines, " "+keyStyle.Render(pad(pair[0], 12))+" "+pair[1])
	}
	return append(lines, "", " "+dimStyle.Render("Màn hình này chỉ đọc. Thêm hoặc thu hồi server: sentinelctl server add|revoke."))
}

// detailRows is the most lines the text of the selected line takes, and tableMargin the rows a table
// leaves for what is under it: a blank line, those lines, and the rule and key bar of the screen.
const (
	detailRows  = 2
	tableMargin = detailRows + 3
)

// wrap breaks text into lines of at most width cells, on spaces, and the last of maxLines is cut with a
// "…" if there is more.
func wrap(text string, width, maxLines int) []string {
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		switch {
		case current == "":
			current = word
		case lipgloss.Width(current)+1+lipgloss.Width(word) <= width:
			current += " " + word
		default:
			lines = append(lines, current)
			current = word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) > maxLines {
		lines = append(lines[:maxLines-1], truncate(strings.Join(lines[maxLines-1:], " "), width))
	}
	for index, line := range lines {
		lines[index] = truncate(line, width) // one word longer than the line
	}
	return lines
}
