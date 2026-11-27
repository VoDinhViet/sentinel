package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/fleet"
)

// The parts of the screen of one server, in the order of the keys 1, 2 and 3.
const (
	serverAlertsPart = iota
	serverBackupsPart
	serverChecksPart
	serverHostPart
	serverPartCount
)

// fleetServerScreen shows one server of the fleet: its alerts, backups and uptime checks, read only.
// It keeps the name of the server, not its answer: the answer is looked up in the model on every
// draw, so a new round of the Fleet tab updates the screen.
type fleetServerScreen struct {
	name   string
	part   int
	cursor int
}

// openFleetServer opens the screen of the server under the cursor of the Fleet tab.
func (m manageModel) openFleetServer() (manageModel, tea.Cmd) {
	if m.tab != fleetTab || m.cursor >= len(m.fleet.servers) {
		return m, nil
	}
	name := m.fleet.servers[m.cursor].Server.Name
	return m.push(fleetServerScreen{name: name}).startFleetHost(name)
}

// fleetServerScreenOnShow says the screen of a server is open, and so the answers should stay fresh.
func (m manageModel) fleetServerScreenOnShow() bool {
	_, open := m.top().(fleetServerScreen)
	return open
}

// answer is what the server said in the last round; false when it is no longer in the fleet file.
func (s fleetServerScreen) answer(m manageModel) (fleet.ServerStatus, bool) {
	for _, answer := range m.fleet.servers {
		if answer.Server.Name == s.name {
			return answer, true
		}
	}
	return fleet.ServerStatus{}, false
}

// parts are the tables of the screen: the report of the server, and its machine as last read.
func (s fleetServerScreen) parts(m manageModel, answer fleet.ServerStatus) []manageTab {
	report := answer.Report
	return []manageTab{alertsTabOf(report), backupsTabOf(report, backupKnowledge{}), checksTabOf(report), m.fleetHostTab(s.name)}
}

func (s fleetServerScreen) lines(m manageModel) []manageLine {
	answer, ok := s.answer(m)
	if !ok || answer.Err != nil {
		return nil
	}
	return s.parts(m, answer)[s.part].lines
}

func (s fleetServerScreen) onKey(m manageModel, key, _ string) (manageModel, tea.Cmd) {
	switch key {
	case "?":
		return m.push(fleetServerHelp()), nil
	case "esc", "q":
		return m.pop(), nil
	case "r":
		next, askServers := m.startFleet()
		next, askMachine := next.startFleetHost(s.name)
		return next, tea.Batch(askServers, askMachine)
	case "tab", "right", "l":
		s.part, s.cursor = (s.part+1)%serverPartCount, 0
	case "shift+tab", "left", "h":
		s.part, s.cursor = (s.part+serverPartCount-1)%serverPartCount, 0
	case "1", "2", "3", "4":
		s.part, s.cursor = int(key[0]-'1'), 0
	case "down", "j":
		s.cursor = min(s.cursor+1, max(len(s.lines(m))-1, 0))
	case "up", "k":
		s.cursor = max(s.cursor-1, 0)
	case "g", "home":
		s.cursor = 0
	case "G", "end":
		s.cursor = max(len(s.lines(m))-1, 0)
	}
	return m.replaceTop(s), nil
}

// click selects the line that was clicked.
func (s fleetServerScreen) click(m manageModel, index int) (manageModel, tea.Cmd) {
	s.cursor = min(index, max(len(s.lines(m))-1, 0))
	return m.replaceTop(s), nil
}

// scroll moves the selection by step lines, staying in the list.
func (s fleetServerScreen) scroll(m manageModel, step int) manageModel {
	s.cursor = min(max(s.cursor+step, 0), max(len(s.lines(m))-1, 0))
	return m.replaceTop(s)
}

func (s fleetServerScreen) render(m manageModel) []string {
	answer, ok := s.answer(m)
	if !ok {
		return []string{titleStyle.Render(s.name), "", dimStyle.Render("Server này không còn trong fleet.yaml."), "", m.keyBar(4, "esc", "quay lại")}
	}
	summary := answer.Summary()
	screenLines := []string{s.titleLine(answer, summary), ""}
	if answer.Err != nil {
		screenLines = append(screenLines, severityStyle(sevBad).Render("Không hỏi được server này:"), "")
		screenLines = append(screenLines, strings.Split(wrap(summary.Problem, m.width), "\n")...)
		return append(screenLines, "", m.keyBar(len(screenLines)+1, "r", "hỏi lại", "esc", "quay lại"))
	}

	parts := s.parts(m, answer)
	screenLines = append(screenLines, s.partsBar(m, len(screenLines), parts), "")
	tab := parts[s.part]
	m.cursor = s.cursor // the table draws the selection of the manager; here it is this screen's
	if len(tab.lines) == 0 {
		screenLines = append(screenLines, dimStyle.Render(s.emptyText(m)))
	} else {
		screenLines = append(screenLines, m.renderTable(tab, m.width, max(m.height-9, 3), len(screenLines))...)
		screenLines = append(screenLines, "", dimStyle.Render(truncate(tab.lines[min(s.cursor, len(tab.lines)-1)].detail, m.width)))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "tab", "đổi mục", "↑↓", "chọn", "r", "hỏi lại", "?", "trợ giúp", "esc", "quay lại"))
}

// titleLine is the name of the server, how it is, and where it was asked.
func (s fleetServerScreen) titleLine(answer fleet.ServerStatus, summary fleet.Summary) string {
	state := severityStyle(severityOfFleetState(summary.State)).Render(summary.State)
	info := fmt.Sprintf("%s · %s · hỏi lúc %s", answer.Server.URL, dashIfEmpty(summary.Version), answer.FetchedAt.Local().Format("15:04:05"))
	return titleStyle.Render(summary.Name) + "  " + state + dimStyle.Render("  "+info)
}

// partsBar is the row of "1 Alerts 2 · 2 Backups 3 · 3 Uptime", the open part filled, a part with a
// problem red. Each is a click target that presses its key.
func (s fleetServerScreen) partsBar(m manageModel, row int, parts []manageTab) string {
	var pills []string
	column := 0
	for i, part := range parts {
		label := fmt.Sprintf(" %d %s", i+1, part.title)
		if part.count > 0 {
			label += fmt.Sprintf(" %d", part.count)
		}
		label += " "
		m.hits.add(row, column, len([]rune(label)), fmt.Sprint(i+1))
		column += len([]rune(label)) + 1
		switch {
		case i == s.part:
			pills = append(pills, activeTab.Render(label))
		case part.alarm:
			pills = append(pills, severityStyle(sevBad).Render(label))
		default:
			pills = append(pills, dimStyle.Render(label))
		}
	}
	return strings.Join(pills, " ")
}

func (s fleetServerScreen) emptyText(m manageModel) string {
	switch s.part {
	case serverHostPart:
		return m.fleetHostWaitText(s.name)
	case serverAlertsPart:
		return "Server này không có alert nào đang mở."
	case serverBackupsPart:
		return "Server này chưa có lần backup nào."
	}
	return "Server này chưa có kết quả uptime check nào."
}

// fleetServerHelp is the key list of the screen of one server.
func fleetServerHelp() helpScreen {
	return helpScreen{title: "Server trong fleet", keys: []keyHelp{
		{"tab  1-4", "chuyển giữa Alerts, Backups, Uptime và Máy (CPU, RAM, đĩa, dịch vụ) của server này"},
		{"↑ ↓  j k", "chọn một dòng"},
		{"r", "hỏi lại server và đọc lại tình trạng máy ngay (màn hình cũng tự làm vậy mỗi 5 giây)"},
		{"click  wheel", "bấm vào mục hoặc dòng; cuộn bằng wheel"},
		{"esc  q", "quay lại danh sách server"},
	}}
}
