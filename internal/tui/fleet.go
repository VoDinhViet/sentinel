package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/fleet"
)

// fleetTab is the index of the Fleet tab: the other servers, read from their daemons' APIs.
const fleetTab = 6

// fleetReadTimeout bounds one round of calls: each server has its own shorter limit, this ends the
// round when the file or the network hangs.
const fleetReadTimeout = 30 * time.Second

// fleetReadMsg carries the round started by startFleet.
type fleetReadMsg struct {
	servers []fleet.ServerStatus
	err     error // the fleet file could not be read; no server was asked
	at      time.Time
}

// fleetState is what the Fleet tab shows: nothing yet, a round in progress, or the last round.
type fleetState struct {
	started bool // asked at least once; the first round starts when the tab is first opened
	reading bool
	servers []fleet.ServerStatus
	err     error
	readAt  time.Time
}

// startFleetIfNeeded starts the first round when the Fleet tab is on screen. Update calls it after
// every message, so every way into the tab starts it.
func (m manageModel) startFleetIfNeeded(cmd tea.Cmd) (manageModel, tea.Cmd) {
	if !m.onTabs() || m.tab != fleetTab || m.fleet.started {
		return m, cmd
	}
	next, start := m.startFleet()
	return next, tea.Batch(cmd, start)
}

// startFleet asks the servers in the background, unless a round is already under way. The timed
// refresh and the r key both come here, so rounds never pile up behind a slow server.
func (m manageModel) startFleet() (manageModel, tea.Cmd) {
	if m.actions.fleet == nil || m.fleet.reading {
		return m, nil
	}
	m.fleet.started, m.fleet.reading = true, true
	poll, ctx := m.actions.fleet, m.ctx
	return m, func() tea.Msg {
		pollCtx, cancel := context.WithTimeout(ctx, fleetReadTimeout)
		defer cancel()
		servers, err := poll(pollCtx)
		return fleetReadMsg{servers: servers, err: err, at: time.Now()}
	}
}

// refreshFleet is what the timed refresh does for the servers: ask again, but only while the tab or
// the screen of a server is on show.
func (m manageModel) refreshFleet() (manageModel, tea.Cmd) {
	if !m.fleetOnShow() {
		return m, nil
	}
	return m.startFleet()
}

// fleetOnShow says the Fleet tab or the screen of one server is what the person is looking at.
func (m manageModel) fleetOnShow() bool {
	onTab := m.onTabs() && m.tab == fleetTab
	return onTab || m.fleetServerScreenOnShow()
}

// afterFleetRead keeps the answers of a round, and the cursor on a line that still exists.
func (m manageModel) afterFleetRead(msg fleetReadMsg) manageModel {
	m.fleet.reading, m.fleet.servers, m.fleet.err, m.fleet.readAt = false, msg.servers, msg.err, msg.at
	m.cursor = min(m.cursor, max(len(m.tabs()[m.tab].lines)-1, 0))
	return m
}

// buildFleetTab is one line per server, in the order of the fleet file.
func (m manageModel) buildFleetTab() manageTab {
	tab := manageTab{
		title:     "Fleet",
		headers:   []string{"SERVER", "TRẠNG THÁI", "VERSION", "ALERT", "BACKUP LỖI", "CHECK DOWN", "ĐANG CHẠY"},
		statusCol: 1, historyCol: -1,
	}
	for _, answer := range m.fleet.servers {
		tab.lines = append(tab.lines, fleetLineOf(answer))
	}
	for _, line := range tab.lines {
		if line.severity != sevOK {
			tab.count++
		}
	}
	tab.alarm = tab.count > 0
	return tab
}

// fleetLineOf is the line of one server. What the server sent is already cleaned by Summary.
func fleetLineOf(answer fleet.ServerStatus) manageLine {
	summary := answer.Summary()
	line := manageLine{
		cells: []string{summary.Name, summary.State, dashIfEmpty(summary.Version), countCell(summary.Alerts), countCell(summary.FailedBackups),
			countCell(summary.DownChecks), countCell(summary.Running)},
		severity: severityOfFleetState(summary.State),
		detail:   summary.Name + ": " + fleetStateSentence(summary),
		fields: [][2]string{
			{"Server", summary.Name}, {"Địa chỉ", answer.Server.URL}, {"Trạng thái", summary.State}, {"Version", dashIfEmpty(summary.Version)},
			{"Alert", fmt.Sprint(summary.Alerts)}, {"Backup lỗi", fmt.Sprint(summary.FailedBackups)}, {"Check down", fmt.Sprint(summary.DownChecks)},
			{"Đang chạy", fmt.Sprint(summary.Running)}, {"Hỏi lúc", answer.FetchedAt.Local().Format("15:04:05")},
		},
	}
	if summary.Problem != "" {
		line.sub = summary.Problem
	}
	return line
}

func severityOfFleetState(state string) severity {
	if state == fleet.StateOK {
		return sevOK
	}
	return sevBad
}

// fleetStateSentence says in words how the server is.
func fleetStateSentence(summary fleet.Summary) string {
	switch summary.State {
	case fleet.StateUnreachable:
		return "không hỏi được: " + summary.Problem
	case fleet.StateAlert:
		return fmt.Sprintf("%d alert đang mở, %d backup lỗi, %d uptime check down", summary.Alerts, summary.FailedBackups, summary.DownChecks)
	}
	return "ổn, không có alert nào đang mở"
}

// countCell is a count for a table cell, "-" for none so that the numbers that matter stand out.
func countCell(count int) string {
	if count == 0 {
		return "-"
	}
	return fmt.Sprint(count)
}

func dashIfEmpty(text string) string {
	if text == "" {
		return "-"
	}
	return text
}

// fleetWaitText is what the tab says while it has no line to show.
func (m manageModel) fleetWaitText() string {
	switch {
	case m.actions.fleet == nil:
		return "Không đọc được danh sách server."
	case errors.Is(m.fleet.err, fleet.ErrNoFile):
		return "Chưa có danh sách server. Tạo file " + fleet.DefaultPath() + " (xem README, mục \"Nhiều server\")."
	case m.fleet.err != nil:
		return "Không đọc được danh sách server: " + m.fleet.err.Error()
	case m.fleet.started && !m.fleet.reading:
		return "Danh sách server trống."
	}
	return "Đang hỏi các server…"
}
