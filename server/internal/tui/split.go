package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"sentinel/server/internal/format"
)

// The wide layout: the list on the left, what is known of the selected server on the right. A terminal
// narrower than splitMinWidth keeps the one-column layout, where a server opens as a screen of its own.
const (
	splitMinWidth = 110
	minPanel      = 3 // the least rows a panel is given: its bar, the header of its table and a line
	listHeadRows  = 3 // the rows above the two columns: the title, the tabs and the rule
)

// wide is whether the terminal has room for the two columns.
func (m model) wide() bool { return m.width >= splitMinWidth }

// showsServer is whether m.open is on show: the screen of a server in the narrow layout, or the selected
// server in the wide one.
func (m model) showsServer() bool {
	return m.opened || (m.wide() && m.tab == serversTab && m.open.name != "")
}

// leftWidth is the room of the list: a third of the terminal, between 36 and 44 cells (a name, a status
// and "10 giây trước" fit in 36).
func leftWidth(width int) int { return min(max(width/3, 36), 44) }

// listColumns are the columns of the tables the list keeps: the rest is said on the right.
var (
	serverListColumns = []int{0, 1, 2} // name, status, last seen
	alertListColumns  = []int{0, 2}    // key, since
)

// splitLines is the whole screen of the wide layout above its closing rule and key bar.
func (m model) splitLines() []string {
	left := leftWidth(m.width)
	right := max(m.width-left-1, 1)
	rows := max(m.height-2-listHeadRows, 0)
	tabs := " " + strings.Join(m.tabPills(), " ")
	lines := []string{
		m.header("sentinelctl", "", levelNone),
		m.joinColumns(left, tabs, m.detailHeading(right)),
		m.splitRule("┼"),
	}
	listLines, detailLines := m.listColumn(left, rows), m.detailColumn(right, rows)
	for row := range rows {
		lines = append(lines, m.joinColumns(left, lineAt(listLines, row), lineAt(detailLines, row)))
	}
	return lines
}

func lineAt(lines []string, row int) string {
	if row < len(lines) {
		return lines[row]
	}
	return ""
}

// joinColumns puts a line of the list and a line of the detail side by side, the list cut to its width.
func (m model) joinColumns(left int, listLine, detailLine string) string {
	cut := lipgloss.NewStyle().MaxWidth(left).Render(listLine)
	return pad(cut, left) + ruleStyle.Render("│") + detailLine
}

// splitRule is a horizontal rule over the whole width, with joint where the two columns meet.
func (m model) splitRule(joint string) string {
	left := leftWidth(m.width)
	return ruleStyle.Render(strings.Repeat("─", left) + joint + strings.Repeat("─", max(m.width-left-1, 0)))
}

// tabPills are the two tabs of the list.
func (m model) tabPills() []string {
	troubledServers := 0
	for _, server := range m.servers {
		if server.Status == "OFFLINE" {
			troubledServers++
		}
	}
	return []string{
		tabPill(m.tab == serversTab, 1, "Server", troubledServers),
		tabPill(m.tab == alertsTab, 2, "Alert", len(m.alerts)),
	}
}

// listColumn is the list of the tab on show, cut down to the columns that fit, and the error of the last ask.
func (m model) listColumn(width, rows int) []string {
	columns := serverListColumns
	if m.tab == alertsTab {
		columns = alertListColumns
	}
	var errLines []string
	if m.loadErr != nil {
		for _, text := range wrap(m.loadErr.Error(), max(width-2, 10), 2) {
			errLines = append(errLines, " "+levelStyle(levelBad).Render(text))
		}
	}
	lines := m.tabTable().only(columns...).render(m.cursor, width, max(rows-len(errLines), 1))
	return append(lines, errLines...)
}

// detailHeading is the line next to the tabs: which server, and how it is, or which alert.
func (m model) detailHeading(width int) string {
	switch {
	case m.tab == alertsTab:
		return " " + dimStyle.Render("Alert đang chọn")
	case m.open.name == "":
		return " " + dimStyle.Render("Chưa có server nào")
	}
	status := format.Status(m.open.detail.Status, m.open.detail.State)
	return " " + titleStyle.Render(truncate(m.open.name, max(width-len(status)-4, 4))) + "  " + levelStyle(statusLevel(m.open.detail.Status)).Render(truncate(status, 24))
}

// detailColumn is the right side: the full text of the selected alert, or the panels of the selected server.
func (m model) detailColumn(width, rows int) []string {
	if m.tab == alertsTab {
		return m.alertDetailLines(width, rows)
	}
	if m.open.name == "" {
		return []string{" " + dimStyle.Render(truncate(m.tabTable().empty, width-2))}
	}
	now := m.now()
	lines := []string{" " + dimStyle.Render(m.serverInfo(now, width))}
	if m.open.err != nil {
		lines = append(lines, " "+levelStyle(levelBad).Render(truncate("Không đọc được server này: "+m.open.err.Error(), width-2)))
	}
	if text := m.open.noticeLine(width); text != "" {
		lines = append(lines, text)
	}
	return append(lines, m.panelLines(width, rows-len(lines))...)
}

// alertDetailLines is the whole text of the alert under the cursor, wrapped.
func (m model) alertDetailLines(width, rows int) []string {
	current := m.tabTable()
	if len(current.lines) == 0 {
		return []string{" " + dimStyle.Render(truncate(current.empty, width-2))}
	}
	var lines []string
	for _, text := range wrap(current.lines[min(m.cursor, len(current.lines)-1)].detail, max(width-2, 10), max(rows, 1)) {
		lines = append(lines, " "+text)
	}
	return lines
}

// panelLines are the five panels of the selected server one under the other, the one with the focus
// first in line for room.
func (m model) panelLines(width, space int) []string {
	parts := m.open.parts(m.now())
	want := make([]int, len(parts))
	for index, part := range parts {
		want[index] = naturalHeight(part)
	}
	focused := -1
	if m.inPanels {
		focused = m.open.current
	}
	var lines []string
	for index, height := range allocate(want, space, focused) {
		if height == 0 {
			continue
		}
		cursor := -1 // no selected line in a panel without the focus
		if index == focused {
			cursor = m.open.cursor
		}
		lines = append(lines, panel(parts[index], index, index == focused, cursor, width, height)...)
	}
	return lines
}

// naturalHeight is the rows a table needs to show everything: its bar, its header and its lines.
func naturalHeight(part table) int {
	if len(part.lines) == 0 {
		return 2
	}
	return len(part.lines) + 2
}

// allocate shares space rows among panels that each want a height. Every panel gets its least first (the
// focused one before the others, so that when space runs out the last ones get nothing), then the rest
// goes one row at a time to each panel that wants more, the focused one twice per round. The heights
// add up to space, or to what the panels want if that is less.
func allocate(want []int, space, focused int) []int {
	heights := make([]int, len(want))
	order := make([]int, 0, len(want)+1)
	if focused >= 0 && focused < len(want) {
		order = append(order, focused)
	}
	for index := range want {
		if index != focused {
			order = append(order, index)
		}
	}
	left := max(space, 0)
	for _, index := range order {
		heights[index] = min(min(want[index], minPanel), left)
		left -= heights[index]
	}
	rounds := order
	if focused >= 0 && focused < len(want) {
		rounds = append([]int{focused}, order...)
	}
	for left > 0 {
		grew := false
		for _, index := range rounds {
			if left > 0 && heights[index] < want[index] {
				heights[index]++
				left--
				grew = true
			}
		}
		if !grew {
			break
		}
	}
	return heights
}

// panel is a bar with the number and title of a part (and how many of its lines are in trouble) over its
// table, height rows in all.
func panel(part table, index int, focused bool, cursor, width, height int) []string {
	title := tabPill(focused, index+1, part.title, part.troubled())
	bar := " " + title + " " + ruleStyle.Render(strings.Repeat("─", max(width-lipgloss.Width(title)-2, 0)))
	body := part.render(cursor, width, height-1)
	return append([]string{bar}, body[:min(len(body), height-1)]...)
}

// splitKeys is the key bar of the wide layout: the keys of whatever has the focus.
func (m model) splitKeys() string {
	switch {
	case m.inPanels && m.open.confirm != nil:
		return keyBar(m.width, "y", "đồng ý", "phím khác", "bỏ qua")
	case m.inPanels && m.open.current == backupsPart:
		return keyBar(m.width, "tab", "khung kế", "1-5", "khung", "↑↓", "chọn dòng", "b", "chạy backup", "s", "dừng", "esc", "về danh sách", "?", "trợ giúp")
	case m.inPanels:
		return keyBar(m.width, "tab", "khung kế", "1-5", "khung", "↑↓", "chọn dòng", "r", "hỏi lại", "esc", "về danh sách", "?", "trợ giúp")
	case m.tab == alertsTab:
		return keyBar(m.width, "↑↓", "chọn alert", "1 2", "đổi tab", "r", "hỏi lại", "?", "trợ giúp", "q", "thoát")
	}
	return keyBar(m.width, "↑↓", "chọn server", "tab", "vào các khung", "1 2", "đổi tab", "r", "hỏi lại", "?", "trợ giúp", "q", "thoát")
}

// syncSelection makes the right side the server under the cursor of the list: when the cursor stands on
// another server than the one on show, its screen starts over and is asked for.
func (m model) syncSelection() (model, tea.Cmd) {
	if !m.wide() || m.tab != serversTab {
		return m, nil
	}
	if m.cursor >= len(m.servers) {
		m.open, m.inPanels = serverScreen{}, false
		return m, nil
	}
	server := m.servers[m.cursor]
	if m.open.name == server.Name {
		return m, nil
	}
	m.open = serverScreen{name: server.Name, loading: true}
	m.open.detail.Agent = server // the line of the list is shown until the report arrives
	return m, m.askDetail(server.Name)
}

// changeLayout starts the screens over when the terminal crosses splitMinWidth, so that none of them
// comes back with a half-answered question.
func (m model) changeLayout() (model, tea.Cmd) {
	m.opened, m.inPanels, m.open = false, false, serverScreen{}
	return m.syncSelection()
}
