package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/ctlapi"
)

// wideModel is a model of a terminal of width x height that has read the list and the first server.
func wideModel(t *testing.T, source *fakeSource, width, height int) model {
	t.Helper()
	m := start(t, source)
	next, ask := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	if ask == nil {
		t.Fatal("a wide terminal did not ask for the selected server")
	}
	return deliver(t, next.(model), ask)
}

func TestAWideTerminalShowsTheListAndTheSelectedServerSideBySide(t *testing.T) {
	m := wideModel(t, twoServers(), 120, 40)
	screen := screenOf(m)
	for _, want := range []string{"1 Server", "2 Alert", "web01", "db01", "web01  ONLINE", "1 Máy", "2 Backup", "3 Uptime", "4 Alert", "5 Việc", "CPU", "files", "site", "ổ /data gần đầy"} {
		if !strings.Contains(screen, want) {
			t.Errorf("no %q on the screen:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "PHIÊN BẢN") {
		t.Errorf("the list kept the columns the right side says in words:\n%s", screen)
	}
	if !strings.Contains(strings.Split(screen, "\n")[1], "│") || !strings.Contains(strings.Split(screen, "\n")[2], "┼") {
		t.Errorf("the two columns are not divided:\n%s", screen)
	}
}

func TestANarrowTerminalKeepsTheOneColumnLayout(t *testing.T) {
	m := start(t, twoServers())
	if strings.Contains(screenOf(m), "│") || strings.Contains(screenOf(m), "1 Máy") {
		t.Errorf("a 100 column terminal got two columns:\n%s", screenOf(m))
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: splitMinWidth - 1, Height: 30})
	if m = next.(model); m.wide() || strings.Contains(screenOf(m), "│") {
		t.Errorf("%d columns is wide", splitMinWidth-1)
	}
}

func TestEveryLineOfTheWideLayoutFitsTheTerminal(t *testing.T) {
	source := twoServers()
	long := strings.Repeat("long-name-", 10)
	source.details[long] = source.details["web01"]
	source.servers[0].Name = long
	source.alerts[0].Message = strings.Repeat("không báo cáo ", 40)
	source.tasks = []ctlapi.Task{{ID: 1, Action: agentapi.ActionBackupStart, State: agentapi.TaskFailed, Error: strings.Repeat("lỗi ", 80)}}
	for _, width := range []int{110, 120, 160, 250} {
		for _, height := range []int{4, 10, 24, 40} {
			m := wideModel(t, source, width, height)
			screens := []string{screenOf(m)}
			m, _ = press(t, m, "enter")
			for part := range partCount {
				m, _ = press(t, m, string(rune('1'+part)))
				screens = append(screens, screenOf(m))
			}
			m, _ = press(t, m, "b", "esc", "2")
			screens = append(screens, screenOf(m))
			for _, screen := range screens {
				lines := strings.Split(screen, "\n")
				if len(lines) > height {
					t.Errorf("%dx%d: %d lines", width, height, len(lines))
				}
				for _, line := range lines {
					if lipgloss.Width(line) > width {
						t.Errorf("%dx%d: a line of %d cells: %q", width, height, lipgloss.Width(line), line)
					}
				}
			}
		}
	}
}

func TestAllocateSharesTheRowsByWhatEachPanelWants(t *testing.T) {
	tests := []struct {
		name    string
		want    []int
		space   int
		focused int
		heights []int
	}{
		{"everything fits", []int{5, 4, 2}, 40, -1, []int{5, 4, 2}},
		{"the least each first, then rounds", []int{20, 20, 20}, 30, -1, []int{10, 10, 10}},
		{"the focused one gets a second share", []int{20, 20, 20}, 30, 1, []int{8, 14, 8}},
		{"a small panel is given all it wants", []int{2, 30, 30}, 20, -1, []int{2, 9, 9}},
		{"no room for the last ones", []int{9, 9, 9, 9}, 7, -1, []int{3, 3, 1, 0}},
		{"the focused one is served first when room runs out", []int{9, 9, 9, 9}, 7, 3, []int{3, 1, 0, 3}},
		{"no room at all", []int{9, 9}, 0, 0, []int{0, 0}},
		{"a negative space", []int{9, 9}, -3, -1, []int{0, 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			heights := allocate(test.want, test.space, test.focused)
			if !slices.Equal(heights, test.heights) {
				t.Errorf("allocate(%v, %d, %d) = %v, want %v", test.want, test.space, test.focused, heights, test.heights)
			}
			sum, wanted := 0, 0
			for index, height := range heights {
				sum += height
				wanted += test.want[index]
				if height > test.want[index] {
					t.Errorf("panel %d got %d of the %d it wanted", index, height, test.want[index])
				}
			}
			if sum != min(max(test.space, 0), wanted) {
				t.Errorf("the heights add up to %d, with %d rows and %d wanted", sum, test.space, wanted)
			}
		})
	}
}

func TestMovingTheCursorOfTheListSelectsTheServerAndAsksForItOnce(t *testing.T) {
	source := twoServers()
	m := wideModel(t, source, 120, 40)
	before := source.calls.Load()
	m, ask := press(t, m, "down")
	if m.open.name != "db01" || !m.open.loading {
		t.Fatalf("selected %q, loading %v", m.open.name, m.open.loading)
	}
	if ask == nil {
		t.Fatal("no ask for the new server")
	}
	m = deliver(t, m, ask)
	if asked := source.calls.Load() - before; asked != 3 { // the server, its tasks and its history: one ask
		t.Errorf("%d calls to the control plane for one move", asked)
	}
	if screen := screenOf(m); !strings.Contains(screen, "db01  OFFLINE") || !strings.Contains(screen, "Chưa nhận báo cáo nào") || strings.Contains(screen, "ổ /data gần đầy") {
		t.Errorf("the right side is not db01:\n%s", screen)
	}
	if _, ask = press(t, m, "down"); ask != nil {
		t.Error("a key that did not move the cursor asked again")
	}
}

func TestTheAnswerForTheServerThatWasLeftIsDropped(t *testing.T) {
	source := twoServers()
	m := start(t, source)
	next, ask := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(model)
	late := ask() // the answer for web01, which arrives after the person moved on
	m, ask = press(t, m, "down")
	next, _ = m.Update(late)
	m = next.(model)
	if m.open.name != "db01" || m.open.detail.Report != nil || !m.open.loading {
		t.Fatalf("the answer for web01 reached db01: %+v", m.open)
	}
	m = deliver(t, m, ask)
	if m.open.loading || m.open.name != "db01" {
		t.Errorf("the answer for db01 was not taken: %+v", m.open)
	}
}

func TestTheRefreshAsksForTheSelectedServerOnly(t *testing.T) {
	source := twoServers()
	m := wideModel(t, source, 120, 40)
	next, ask := m.Update(refreshMsg{})
	m = next.(model)
	if !m.loading || !m.open.loading || ask == nil {
		t.Errorf("the refresh did not ask for the list and the server: list %v, server %v", m.loading, m.open.loading)
	}
}

func TestTheFocusMovesBetweenTheListAndThePanels(t *testing.T) {
	m := wideModel(t, twoServers(), 120, 40)
	if m.inPanels {
		t.Fatal("the focus starts in a panel")
	}
	steps := []struct {
		keys     []string
		inPanels bool
		part     int
	}{
		{[]string{"tab"}, true, hostPart},
		{[]string{"tab"}, true, backupsPart},
		{[]string{"right"}, true, checksPart},
		{[]string{"shift+tab"}, true, backupsPart},
		{[]string{"4"}, true, alertsPart},
		{[]string{"1"}, true, hostPart},
		{[]string{"left"}, false, hostPart}, // before the first panel is the list
		{[]string{"enter"}, true, hostPart},
		{[]string{"5", "tab"}, false, tasksPart}, // after the last panel is the list
		{[]string{"enter", "3", "esc"}, false, checksPart},
	}
	for _, step := range steps {
		m, _ = press(t, m, step.keys...)
		if m.inPanels != step.inPanels || (step.inPanels && m.open.current != step.part) {
			t.Fatalf("after %v: in a panel %v, part %d; want %v, %d", step.keys, m.inPanels, m.open.current, step.inPanels, step.part)
		}
	}
}

func TestInAPanelTheArrowsMoveTheLineAndNotTheServer(t *testing.T) {
	m := wideModel(t, twoServers(), 120, 40)
	m, ask := press(t, m, "enter", "2", "down")
	if ask != nil || m.cursor != 0 || m.open.name != "web01" || m.open.cursor != 1 {
		t.Errorf("list cursor %d, server %s, line %d, ask %v", m.cursor, m.open.name, m.open.cursor, ask != nil)
	}
}

func TestOnTheListQuitsAndInAPanelEscGoesBack(t *testing.T) {
	m := wideModel(t, twoServers(), 120, 40)
	m, quit := press(t, m, "enter", "esc")
	if quit != nil || m.inPanels {
		t.Fatalf("esc in a panel: quit %v, still in it %v", quit != nil, m.inPanels)
	}
	if _, quit = press(t, m, "esc"); quit == nil {
		t.Error("esc on the list did not quit")
	}
}

func TestBAsksOnlyInTheBackupPanelAndSendsAfterY(t *testing.T) {
	source := twoServers()
	m := wideModel(t, source, 120, 40)
	for _, keys := range [][]string{{"b"}, {"enter", "b"}, {"enter", "3", "s"}} { // the list, the panel Máy, the panel Uptime
		m, _ = press(t, m, keys...)
		if m.open.confirm != nil {
			t.Fatalf("%v asked a question outside the Backup panel", keys)
		}
		m, _ = press(t, m, "esc")
	}
	m, _ = press(t, m, "enter", "2")
	m, ask := press(t, m, "b")
	if ask != nil || len(source.given) != 0 || !strings.Contains(screenOf(m), "Chạy backup job files vào repository s3 trên web01?") {
		t.Fatalf("b sent before y or did not ask:\n%s", screenOf(m))
	}
	if !strings.Contains(strings.Split(screenOf(m), "\n")[39], "y") {
		t.Errorf("the key bar does not say y:\n%s", screenOf(m))
	}
	m, ask = press(t, m, "y")
	m = deliver(t, m, ask)
	if len(source.given) != 1 || source.given[0].agent != "web01" || source.given[0].action != agentapi.ActionBackupStart {
		t.Fatalf("given = %+v", source.given)
	}
	if !strings.Contains(screenOf(m), "Đã giao việc #1 (backup.start)") {
		t.Errorf("the person is not told:\n%s", screenOf(m))
	}
}

func TestAQuestionIsDroppedWhenAnotherKeyAnswersIt(t *testing.T) {
	source := twoServers()
	m := wideModel(t, source, 120, 40)
	m, _ = press(t, m, "enter", "2", "s")
	if m.open.confirm == nil {
		t.Fatal("no question")
	}
	m, ask := press(t, m, "down")
	if ask != nil || m.open.confirm != nil || len(source.given) != 0 {
		t.Errorf("sent %+v, still asking %v", source.given, m.open.confirm != nil)
	}
}

func TestTheAlertTabShowsTheWholeAlertOnTheRight(t *testing.T) {
	source := twoServers()
	source.alerts[0].Message = "db01 không báo cáo " + strings.Repeat("rất lâu ", 12) + "xong"
	m := wideModel(t, source, 120, 40)
	m, _ = press(t, m, "2")
	screen := screenOf(m)
	if !strings.Contains(screen, "agent:db01") || !strings.Contains(screen, "Alert đang chọn") || !strings.Contains(screen, "xong") {
		t.Errorf("the alert is not whole on the right:\n%s", screen)
	}
	if strings.Contains(screen, "1 Máy") || m.showsServer() {
		t.Errorf("the panels of a server show on the alert tab:\n%s", screen)
	}
	m, _ = press(t, m, "1")
	if m.open.name != "web01" || !strings.Contains(screenOf(m), "1 Máy") {
		t.Errorf("back on the servers: %q\n%s", m.open.name, screenOf(m))
	}
}

func TestTheListOfANewControlPlaneSaysHowToAddAServer(t *testing.T) {
	next, _ := start(t, &fakeSource{}).Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m := next.(model)
	screen := screenOf(m)
	if !strings.Contains(screen, "Chưa có server nào") || m.showsServer() {
		t.Errorf("no empty list:\n%s", screen)
	}
	if m, _ = press(t, m, "enter", "tab", "down"); m.inPanels {
		t.Error("the focus went to a panel of a server that does not exist")
	}
}

func TestResizingAcrossTheLimitStartsTheScreensOver(t *testing.T) {
	source := twoServers()
	m := wideModel(t, source, 120, 40)
	m, _ = press(t, m, "enter", "2", "b")
	if m.open.confirm == nil {
		t.Fatal("no question")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 40})
	m = next.(model)
	if m.inPanels || m.opened || m.open.name != "" || m.open.confirm != nil {
		t.Errorf("the narrow screen kept the state of the wide one: %+v", m)
	}
	if _, ask := press(t, m, "y"); ask != nil {
		t.Error("a y after the resize sent a task")
	}
	next, ask := m.Update(tea.WindowSizeMsg{Width: 130, Height: 40})
	if m = next.(model); m.open.name != "web01" || ask == nil {
		t.Errorf("widening again did not select the server under the cursor: %q", m.open.name)
	}
}

func TestTheHelpListsTheKeysOfTheWideLayout(t *testing.T) {
	m := wideModel(t, twoServers(), 120, 40)
	m, _ = press(t, m, "?")
	if screen := screenOf(m); !strings.Contains(screen, "110 cột") || strings.Contains(screen, "┴") {
		t.Errorf("help:\n%s", screen)
	}
	if m, _ = press(t, m, "x"); m.help {
		t.Error("a key did not close the help")
	}
}
