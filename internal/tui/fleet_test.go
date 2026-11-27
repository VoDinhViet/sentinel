package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sentinel/internal/fleet"
	"sentinel/internal/status"
)

// threeFleetServers is a server that is fine, one with an open alert and one nobody can reach.
func threeFleetServers() []fleet.ServerStatus {
	now := time.Now()
	return []fleet.ServerStatus{
		{Server: fleet.Server{Name: "web-01", URL: "https://web-01:9311"}, FetchedAt: now, Report: status.Report{Version: "v1.2.0", Server: "web-01"}},
		{Server: fleet.Server{Name: "db-01", URL: "http://10.0.0.5:9311"}, FetchedAt: now, Report: status.Report{
			Version: "v1.1.0", Alerts: []status.Alert{{Key: "stale:shop"}}, Jobs: []status.Job{{Job: "shop", Success: false}}, Running: []status.Running{{Job: "x"}},
		}},
		{Server: fleet.Server{Name: "edge"}, FetchedAt: now, Err: errors.New("server edge: connection refused\x1b[2J")},
	}
}

// fleetModel is a loaded screen whose fleet action counts its rounds and answers with servers and err.
func fleetModel(t *testing.T, servers []fleet.ServerStatus, err error) (manageModel, *int) {
	t.Helper()
	rounds := new(int)
	actions := manageActions{fleet: func(context.Context) ([]fleet.ServerStatus, error) {
		*rounds++
		return servers, err
	}}
	model, _ := newManageModel(context.Background(), func() (status.Report, error) { return sampleReport(), nil }, actions, nil).Update(reportMsg{report: sampleReport()})
	return model.(manageModel), rounds
}

// openFleetTab presses 7 and delivers the round the tab started.
func openFleetTab(t *testing.T, screen manageModel) manageModel {
	t.Helper()
	screen, cmd := send(t, screen, "7")
	if cmd == nil || !screen.fleet.reading {
		t.Fatal("opening the tab must start a round")
	}
	model, _ := screen.Update(cmd())
	return model.(manageModel)
}

func TestFleetTabAsksOnFirstVisitAndShowsEachServer(t *testing.T) {
	screen, rounds := fleetModel(t, threeFleetServers(), nil)
	if *rounds != 0 {
		t.Fatal("the servers must not be asked before the tab is opened")
	}
	screen, cmd := send(t, screen, "7")
	if view := plain(screen.render()); !strings.Contains(view, "Đang hỏi các server") {
		t.Errorf("no progress text while asking:\n%s", view)
	}
	model, _ := screen.Update(cmd())
	screen = model.(manageModel)
	if *rounds != 1 || screen.fleet.reading {
		t.Fatalf("rounds = %d, reading = %v", *rounds, screen.fleet.reading)
	}
	view := plain(screen.render())
	for _, want := range []string{"web-01", "OK", "v1.2.0", "db-01", "ALERT", "edge", "UNREACHABLE", "connection refused"} {
		if !strings.Contains(view, want) {
			t.Errorf("the tab lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "\x1b[2J") {
		t.Error("an escape sequence of a remote error reached the screen")
	}
}

func TestFleetTabLinesFollowTheFleetFileAndCountTheServersInTrouble(t *testing.T) {
	screen, _ := fleetModel(t, threeFleetServers(), nil)
	tab := openFleetTab(t, screen).tabs()[fleetTab]
	names := []string{tab.lines[0].cells[0], tab.lines[1].cells[0], tab.lines[2].cells[0]}
	if strings.Join(names, ",") != "web-01,db-01,edge" {
		t.Errorf("order = %v", names)
	}
	if levels := []severity{tab.lines[0].severity, tab.lines[1].severity, tab.lines[2].severity}; levels[0] != sevOK || levels[1] != sevBad || levels[2] != sevBad {
		t.Errorf("severities = %v", levels)
	}
	if tab.count != 2 || !tab.alarm {
		t.Errorf("count = %d, alarm = %v, want the 2 servers in trouble", tab.count, tab.alarm)
	}
	// db-01: 1 alert, 1 backup failed, no check down, 1 running
	if cells := tab.lines[1].cells; cells[3] != "1" || cells[4] != "1" || cells[5] != "-" || cells[6] != "1" {
		t.Errorf("db-01 counts = %v", cells)
	}
	allFine, _ := fleetModel(t, threeFleetServers()[:1], nil)
	if tab := openFleetTab(t, allFine).tabs()[fleetTab]; tab.alarm || tab.count != 0 {
		t.Errorf("a fleet that is fine: alarm = %v, count = %d", tab.alarm, tab.count)
	}
}

func TestFleetTabAsksAgainOnRAndOnRefreshWhileOpenOnly(t *testing.T) {
	screen, rounds := fleetModel(t, threeFleetServers(), nil)
	screen = openFleetTab(t, screen)

	screen, cmd := send(t, screen, "r")
	if cmd == nil || !screen.fleet.reading {
		t.Fatal("r must ask again")
	}
	if _, again := send(t, screen, "r"); again != nil {
		t.Error("a second round must not start while one is under way")
	}
	model, _ := screen.Update(cmd())
	screen = model.(manageModel)
	if *rounds != 2 {
		t.Fatalf("rounds = %d, want 2", *rounds)
	}

	if _, cmd := screen.refreshFleet(); cmd == nil {
		t.Error("the timed refresh must ask again while the tab is open")
	}
	away, _ := send(t, screen, "1")
	if _, cmd := away.refreshFleet(); cmd != nil {
		t.Error("the timed refresh must not ask while another tab is open")
	}
	if _, cmd := send(t, away, "7"); cmd != nil {
		t.Error("coming back must not ask again at once: the tab keeps its answers until the refresh")
	}
}

func TestFleetTabSaysHowToStartWhenThereIsNoFile(t *testing.T) {
	noFile := fmt.Errorf("%w: /home/me/.config/sentinel/fleet.yaml; create it", fleet.ErrNoFile)
	screen, _ := fleetModel(t, nil, noFile)
	view := plain(openFleetTab(t, screen).render())
	if !strings.Contains(view, "Chưa có danh sách server") || !strings.Contains(view, "fleet.yaml") {
		t.Errorf("no hint to create the file:\n%s", view)
	}
	broken, _ := fleetModel(t, nil, errors.New("fleet.yaml: servers[0]: token is empty"))
	if view := plain(openFleetTab(t, broken).render()); !strings.Contains(view, "token is empty") {
		t.Errorf("the reason the file is refused is not shown:\n%s", view)
	}
}

func TestFleetTabWithoutAnActionSaysSo(t *testing.T) {
	model, _ := newManageModel(context.Background(), func() (status.Report, error) { return sampleReport(), nil }, manageActions{}, nil).Update(reportMsg{report: sampleReport()})
	screen, cmd := send(t, model.(manageModel), "7")
	if cmd != nil {
		t.Error("nothing to ask: no command expected")
	}
	if view := plain(screen.render()); !strings.Contains(view, "Không đọc được danh sách server") {
		t.Errorf("the tab does not say it cannot work:\n%s", view)
	}
}
