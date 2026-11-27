package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/agent/internal/fleet"
)

// fleetWithDetails is a server that is fine, one with an alert, a failed backup and a check down,
// and one nobody can reach.
func fleetWithDetails() []fleet.ServerStatus {
	servers := threeFleetServers()
	report := remoteReport()
	report.Version = "v1.1.0"
	servers[1].Report = report
	return servers
}

// openServerScreen opens the Fleet tab, and the screen of the server at index.
func openServerScreen(t *testing.T, index int) (manageModel, *int) {
	t.Helper()
	screen, rounds := fleetModel(t, fleetWithDetails(), nil)
	screen = openFleetTab(t, screen)
	for range index {
		screen = keys(t, screen, "down")
	}
	screen = keys(t, screen, "enter")
	if _, open := screen.top().(fleetServerScreen); !open {
		t.Fatal("enter on the Fleet tab must open the screen of the server")
	}
	return screen, rounds
}

func TestEnterOnAServerOpensItsAlertsBackupsAndChecks(t *testing.T) {
	screen, _ := openServerScreen(t, 1)
	view := plain(screen.render())
	for _, want := range []string{"db-01", "ALERT", "v1.1.0", "1 Alerts 1", "2 Backups 2", "3 Uptime 2", "4 Máy", "stale:shop", "no backup for 3 days"} {
		if !strings.Contains(view, want) {
			t.Errorf("the screen lacks %q:\n%s", want, view)
		}
	}
	view = plain(keys(t, screen, "2").render())
	for _, want := range []string{"appdb", "shop", "LỖI", "restic exited 1"} {
		if !strings.Contains(view, want) {
			t.Errorf("the Backups part lacks %q:\n%s", want, view)
		}
	}
	view = plain(keys(t, screen, "3").render())
	for _, want := range []string{"site", "UP", "db-port", "DOWN", "refused"} {
		if !strings.Contains(view, want) {
			t.Errorf("the Uptime part lacks %q:\n%s", want, view)
		}
	}
}

func TestTheScreenOfAServerIsReadOnly(t *testing.T) {
	screen, _ := openServerScreen(t, 1)
	screen = keys(t, screen, "2")
	for _, key := range []string{"b", "x", "a", "e", "d", "s", "t", "enter"} {
		model, cmd := screen.onKey(key, "")
		if cmd != nil {
			t.Errorf("%s started something on the screen of a server", key)
		}
		if _, open := model.(manageModel).top().(fleetServerScreen); !open {
			t.Errorf("%s left the screen of the server", key)
		}
	}
}

func TestTheScreenOfAServerMovesAndClosesWithKeys(t *testing.T) {
	screen, _ := openServerScreen(t, 1)
	screen = keys(t, screen, "tab")
	if got := screen.top().(fleetServerScreen).part; got != serverBackupsPart {
		t.Errorf("tab opened part %d, want Backups", got)
	}
	screen = keys(t, screen, "down")
	if got := screen.top().(fleetServerScreen).cursor; got != 1 {
		t.Errorf("cursor = %d, want 1", got)
	}
	screen = keys(t, screen, "down", "down", "down")
	if got := screen.top().(fleetServerScreen).cursor; got != 1 {
		t.Errorf("the cursor left the 2 backups: %d", got)
	}
	screen = keys(t, screen, "shift+tab", "shift+tab")
	if got := screen.top().(fleetServerScreen).part; got != serverHostPart {
		t.Errorf("two steps back from Backups is %d, want the machine (wrapping)", got)
	}
	if screen = keys(t, screen, "esc"); !screen.onTabs() || screen.tab != fleetTab {
		t.Error("esc must go back to the Fleet tab")
	}
}

func TestTheScreenOfAServerStaysFreshWhileItIsOpen(t *testing.T) {
	screen, rounds := openServerScreen(t, 1)
	before := *rounds
	next, cmd := screen.refreshFleet()
	if cmd == nil || !next.fleet.reading {
		t.Fatal("the timed refresh must ask again while a server's screen is open")
	}

	fresh := fleetWithDetails()
	fresh[1].Report.Alerts = nil
	model, _ := next.Update(fleetReadMsg{servers: fresh, at: time.Now()})
	if view := plain(model.(manageModel).render()); strings.Contains(view, "stale:shop") {
		t.Errorf("the screen did not take the new answer:\n%s", view)
	}
	if *rounds != before {
		t.Error("the test must not have run a real round")
	}
	if _, cmd := keys(t, screen, "?").refreshFleet(); cmd != nil {
		t.Error("another screen over the tabs must not ask")
	}
}

func TestTheScreenOfAServerThatCannotBeReachedSaysWhy(t *testing.T) {
	screen, _ := openServerScreen(t, 2)
	view := plain(screen.render())
	if !strings.Contains(view, "UNREACHABLE") || !strings.Contains(view, "Không hỏi được server này") || !strings.Contains(view, "connection refused") {
		t.Errorf("no reason:\n%s", view)
	}
	for _, key := range []string{"tab", "down", "2"} {
		if _, open := keys(t, screen, key).top().(fleetServerScreen); !open {
			t.Errorf("%s must not break the screen of an unreachable server", key)
		}
	}
}

func TestTheScreenOfAServerThatLeftTheFileSaysSo(t *testing.T) {
	screen, _ := openServerScreen(t, 1)
	model, _ := screen.Update(fleetReadMsg{servers: fleetWithDetails()[:1], at: time.Now()})
	view := plain(model.(manageModel).render())
	if !strings.Contains(view, "không còn trong fleet.yaml") {
		t.Errorf("the screen does not say the server left the fleet:\n%s", view)
	}
	if model, _ := model.(manageModel).onKey("esc", ""); !model.(manageModel).onTabs() {
		t.Error("esc must still go back")
	}
}

func TestClicksSelectLinesAndPartsOnTheScreenOfAServer(t *testing.T) {
	screen, _ := openServerScreen(t, 1)
	screen, _ = clickOn(t, screen, "2")
	if got := screen.top().(fleetServerScreen).part; got != serverBackupsPart {
		t.Fatalf("a click on the Backups pill opened part %d", got)
	}
	tabCursor := screen.cursor
	screen, _ = clickOn(t, screen, "row:1")
	if got := screen.top().(fleetServerScreen).cursor; got != 1 {
		t.Errorf("a click on the second backup selected %d", got)
	}
	if screen.cursor != tabCursor {
		t.Errorf("the click moved the cursor of the Fleet tab from %d to %d", tabCursor, screen.cursor)
	}
	screen = wheel(t, screen, tea.MouseWheelUp)
	if got := screen.top().(fleetServerScreen).cursor; got != 0 {
		t.Errorf("wheel up left the cursor at %d", got)
	}
}

func TestADoubleClickOnAServerOpensIt(t *testing.T) {
	screen, _ := fleetModel(t, fleetWithDetails(), nil)
	screen = openFleetTab(t, screen)
	screen, _ = clickOn(t, screen, "row:1")
	if !screen.onTabs() || screen.cursor != 1 {
		t.Fatal("the first click selects the server")
	}
	screen, _ = clickOn(t, screen, "row:1")
	if open, ok := screen.top().(fleetServerScreen); !ok || open.name != "db-01" {
		t.Errorf("the second click must open db-01, top = %v", screen.top())
	}
}

func TestOpeningAServerNeedsALineUnderTheCursor(t *testing.T) {
	broken, _ := fleetModel(t, nil, errors.New("no file"))
	broken = openFleetTab(t, broken)
	if model := keys(t, broken, "enter"); !model.onTabs() {
		t.Error("enter with no server opened a screen")
	}
}
