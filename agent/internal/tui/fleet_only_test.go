package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/agent/internal/fleet"
)

// fleetOnlyModel is the manager of `sentinel fleet` on a large screen, with the first round answered.
func fleetOnlyModel(t *testing.T, servers []fleet.ServerStatus) (manageModel, *int) {
	t.Helper()
	rounds := new(int)
	model := newFleetModel(context.Background(), func(context.Context) ([]fleet.ServerStatus, error) {
		*rounds++
		return servers, nil
	})
	if cmd := model.Init(); cmd == nil {
		t.Fatal("Init must start the first round")
	}
	next, _ := model.Update(tea.WindowSizeMsg{Width: 130, Height: 40})
	next, _ = next.Update(model.askFleet()())
	return next.(manageModel), rounds
}

func TestTheFleetManagerShowsOnlyTheFleetTab(t *testing.T) {
	screen, rounds := fleetOnlyModel(t, fleetWithDetails())
	if *rounds != 1 {
		t.Fatalf("rounds = %d, want the first one only: opening must not start another", *rounds)
	}
	view := plain(screen.render())
	for _, want := range []string{"1 Fleet 2", "web-01", "db-01", "edge", "2 server có vấn đề", "1 ổn", "cập nhật"} {
		if !strings.Contains(view, want) {
			t.Errorf("the screen lacks %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"Backups", "Uptime", "Doctor", "Config", "tổng quan", "đang tải"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("the screen shows %q, which is about this machine:\n%s", unwanted, view)
		}
	}
}

func TestTheFleetManagerKeepsToItsOneTab(t *testing.T) {
	screen, _ := fleetOnlyModel(t, fleetWithDetails())
	for _, key := range []string{"tab", "shift+tab", "right", "left", "1", "2", "3", "4", "5", "6", "7"} {
		if model := keys(t, screen, key); model.tab != fleetTab {
			t.Errorf("%s moved to tab %d", key, model.tab)
		}
	}
}

func TestTheFleetManagerNeverActsOnThisMachine(t *testing.T) {
	screen, _ := fleetOnlyModel(t, fleetWithDetails())
	for _, key := range []string{"b", "x", "a", "e", "d", "t"} {
		model, cmd := screen.onKey(key, "")
		if cmd != nil {
			t.Errorf("%s started something", key)
		}
		if after := model.(manageModel); !after.onTabs() || after.notice != "" {
			t.Errorf("%s opened a screen or said something: %q", key, after.notice)
		}
	}
	if _, cmd := screen.onKey("r", ""); cmd == nil {
		t.Error("r must ask the servers again")
	}
}

func TestTheFleetManagerRefreshesByItself(t *testing.T) {
	screen, _ := fleetOnlyModel(t, fleetWithDetails())
	_, cmd := screen.Update(fleetReadMsg{servers: fleetWithDetails(), at: time.Now()})
	if cmd == nil {
		t.Error("an answer must schedule the next round: there is no report load to do it")
	}
	if next, cmd := screen.refreshFleetOnly(); cmd == nil || !next.fleet.reading {
		t.Error("a tick on the tab must ask again")
	}

	helped := keys(t, screen, "?")
	if _, cmd := helped.refreshFleetOnly(); cmd == nil {
		t.Error("a tick under a help screen asks nothing, but must schedule the next tick or the refresh stops for good")
	}
	busy, _ := screen.startFleet()
	if _, cmd := busy.refreshFleetOnly(); cmd != nil {
		t.Error("a tick during a round must not add a second chain of ticks: the answer schedules the next")
	}
}

func TestTheFleetManagerOpensAServerAndComesBack(t *testing.T) {
	screen, _ := fleetOnlyModel(t, fleetWithDetails())
	screen = keys(t, screen, "down", "enter")
	if open, ok := screen.top().(fleetServerScreen); !ok || open.name != "db-01" {
		t.Fatalf("top = %v", screen.top())
	}
	if !strings.Contains(plain(screen.render()), "stale:shop") {
		t.Error("the screen of the server shows nothing of it")
	}
	screen = keys(t, screen, "esc")
	if !screen.onTabs() || screen.quitting {
		t.Error("esc on the screen of a server must go back, not quit")
	}
	if quit := keys(t, screen, "q"); !quit.quitting {
		t.Error("q on the tab must quit")
	}
}

func TestTheFleetManagerHelpDoesNotOfferOtherTabs(t *testing.T) {
	screen, _ := fleetOnlyModel(t, fleetWithDetails())
	view := plain(keys(t, screen, "?").render())
	if strings.Contains(view, "chuyển giữa Backups") || !strings.Contains(view, "mở server đang chọn") {
		t.Errorf("help:\n%s", view)
	}
}

func TestTheFleetManagerSaysHowToStartWithoutAFile(t *testing.T) {
	model := newFleetModel(context.Background(), func(context.Context) ([]fleet.ServerStatus, error) { return nil, fleet.ErrNoFile })
	next, _ := model.Update(fleetReadMsg{err: fleet.ErrNoFile, at: time.Now()})
	view := plain(next.(manageModel).render())
	if !strings.Contains(view, "Chưa có danh sách server") {
		t.Errorf("no hint:\n%s", view)
	}
}
