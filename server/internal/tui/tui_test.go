package tui

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/client"
	"sentinel/server/internal/ctlapi"
)

var (
	now        = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;:]*[A-Za-z]`)
)

// fakeSource answers what the test put in it, and counts the calls.
type fakeSource struct {
	servers []ctlapi.Agent
	details map[string]ctlapi.AgentDetail
	alerts  []ctlapi.Alert
	err     error
	calls   atomic.Int32

	tasks     []ctlapi.Task
	given     []givenTask // what CreateTask was asked, in order
	createErr error
}

type givenTask struct {
	agent, action string
	params        map[string]string
}

func (f *fakeSource) Agents(context.Context) ([]ctlapi.Agent, error) {
	f.calls.Add(1)
	return f.servers, f.err
}

func (f *fakeSource) Agent(_ context.Context, name string) (ctlapi.AgentDetail, error) {
	f.calls.Add(1)
	return f.details[name], f.err
}

func (f *fakeSource) Alerts(context.Context) ([]ctlapi.Alert, error) {
	f.calls.Add(1)
	return f.alerts, f.err
}

func (f *fakeSource) Tasks(_ context.Context, _ string, _ int) ([]ctlapi.Task, error) {
	f.calls.Add(1)
	return f.tasks, f.err
}

func (f *fakeSource) CreateTask(_ context.Context, agent, action string, params map[string]string) (ctlapi.Task, error) {
	if f.createErr != nil {
		return ctlapi.Task{}, f.createErr
	}
	f.given = append(f.given, givenTask{agent: agent, action: action, params: params})
	return ctlapi.Task{ID: int64(len(f.given)), Agent: agent, Action: action, Params: params, State: agentapi.TaskQueued}, nil
}

func ago(d time.Duration) *time.Time { t := now.Add(-d); return &t }

func twoServers() *fakeSource {
	web := ctlapi.Agent{Name: "web01", State: "active", Status: "ONLINE", LastSeen: ago(10 * time.Second), Version: "1.4.0", CertNotAfter: ago(-60 * 24 * time.Hour)}
	db := ctlapi.Agent{Name: "db01", State: "active", Status: "OFFLINE", LastSeen: ago(2 * time.Hour), Version: "1.3.0"}
	report := agentapi.Report{
		Host: agentapi.HostStatus{
			Hostname: "web01.example.com", OS: "Debian 13", Kernel: "6.12", UptimeSeconds: 90000, CPUs: 4, CPUPercent: 12, MinFreePercent: 10,
			Memory: agentapi.MemoryStatus{TotalBytes: 8 << 30, AvailableBytes: 4 << 30},
			Disks: []agentapi.DiskStatus{
				{Mount: "/", TotalBytes: 100 << 30, FreeBytes: 50 << 30},
				{Mount: "/data", TotalBytes: 100 << 30, FreeBytes: 3 << 30},
			},
			FailedServices: []agentapi.ServiceStatus{{Name: "cron.service", Active: "failed"}},
		},
		Backups: []agentapi.BackupStatus{
			{Job: "files", Repository: "s3", FinishedAt: *ago(time.Hour), Success: true, Result: "ok"},
			{Job: "db", Repository: "s3", FinishedAt: *ago(2 * time.Hour), Success: false, Result: "FAILED: repository is locked"},
		},
		Checks: []agentapi.CheckStatus{{Name: "site", OK: false, Error: "connection refused", At: *ago(time.Minute)}},
		Alerts: []agentapi.AlertStatus{{Key: "disk:/data", Message: "ổ /data gần đầy", Since: *ago(3 * time.Hour)}},
		Allow:  []string{agentapi.ActionBackupStart, agentapi.ActionBackupStop, agentapi.ActionBackupSnapshots},
	}
	return &fakeSource{
		servers: []ctlapi.Agent{web, db},
		details: map[string]ctlapi.AgentDetail{
			"web01": {Agent: web, Report: &report, ReportAt: ago(5 * time.Second)},
			"db01":  {Agent: db},
		},
		alerts: []ctlapi.Alert{{Key: "agent:db01", Message: "db01 không báo cáo", Since: *ago(time.Hour)}},
	}
}

// start is a model that has read the list once, on a 100x30 terminal.
func start(t *testing.T, source *fakeSource) model {
	t.Helper()
	m := newModel(context.Background(), source, func() time.Time { return now })
	m.loading = true
	return deliver(t, m, m.askList())
}

// deliver runs the command (one that asks the source) and gives its answer to the model.
func deliver(t *testing.T, m model, ask tea.Cmd) model {
	t.Helper()
	if ask == nil {
		t.Fatal("no command to run")
	}
	next, _ := m.Update(ask())
	return next.(model)
}

func press(t *testing.T, m model, keys ...string) (model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, key := range keys {
		next, c := m.onKey(key)
		m, cmd = next.(model), c
	}
	return m, cmd
}

func screenOf(m model) string { return ansiEscape.ReplaceAllString(m.render(), "") }

func TestTheListShowsEveryServerWithItsStatusAndWhenItWasSeen(t *testing.T) {
	m := start(t, twoServers())
	screen := screenOf(m)
	for _, want := range []string{"TÊN", "TRẠNG THÁI", "LẦN CUỐI", "web01", "ONLINE", "10 giây trước", "1.4.0", "db01", "OFFLINE", "2 giờ trước", "1 Server 1", "2 Alert 1"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the list does not show %q:\n%s", want, screen)
		}
	}
}

func TestTheLineUnderTheListSaysInWordsHowTheSelectedServerIs(t *testing.T) {
	m := start(t, twoServers())
	if !strings.Contains(screenOf(m), "web01: ONLINE, thấy 10 giây trước, phiên bản 1.4.0, chứng chỉ hết hạn") {
		t.Errorf("no sentence for the selected server:\n%s", screenOf(m))
	}
	m, _ = press(t, m, "down")
	if !strings.Contains(screenOf(m), "db01: OFFLINE") {
		t.Errorf("the sentence did not follow the cursor:\n%s", screenOf(m))
	}
}

func TestACertificateNearItsEndIsSaidInTheSentence(t *testing.T) {
	source := twoServers()
	source.servers[0].CertNotAfter = ago(-3 * 24 * time.Hour)
	m := start(t, source)
	if !strings.Contains(screenOf(m), "sắp hết") {
		t.Errorf("no warning for a certificate with 3 days left:\n%s", screenOf(m))
	}
}

func TestTheCursorStaysInTheList(t *testing.T) {
	m := start(t, twoServers())
	m, _ = press(t, m, "up", "up")
	if m.cursor != 0 {
		t.Errorf("cursor above the first line: %d", m.cursor)
	}
	m, _ = press(t, m, "down", "down", "down", "j")
	if m.cursor != 1 {
		t.Errorf("cursor below the last line: %d", m.cursor)
	}
	m, _ = press(t, m, "g")
	if m.cursor != 0 {
		t.Errorf("g did not go to the top: %d", m.cursor)
	}
	m, _ = press(t, m, "G")
	if m.cursor != 1 {
		t.Errorf("G did not go to the bottom: %d", m.cursor)
	}
}

func TestTabsSwitchAndResetTheCursor(t *testing.T) {
	m := start(t, twoServers())
	m, _ = press(t, m, "down", "tab")
	if m.tab != alertsTab || m.cursor != 0 {
		t.Fatalf("tab = %d, cursor = %d", m.tab, m.cursor)
	}
	if screen := screenOf(m); !strings.Contains(screen, "agent:db01") || !strings.Contains(screen, "db01 không báo cáo") {
		t.Errorf("the alerts tab:\n%s", screen)
	}
	m, _ = press(t, m, "shift+tab")
	if m.tab != serversTab {
		t.Errorf("shift+tab did not go back: %d", m.tab)
	}
	m, _ = press(t, m, "2")
	if m.tab != alertsTab {
		t.Errorf("2 did not open the alerts: %d", m.tab)
	}
}

func TestAnEmptyControlPlaneSaysHowToAddAServer(t *testing.T) {
	m := start(t, &fakeSource{})
	if !strings.Contains(screenOf(m), "sentinelctl server add") {
		t.Errorf("no hint:\n%s", screenOf(m))
	}
	m, _ = press(t, m, "tab")
	if !strings.Contains(screenOf(m), "Không có cảnh báo nào đang mở.") {
		t.Errorf("no word for no alerts:\n%s", screenOf(m))
	}
}

func TestEnterOpensTheServerAndShowsItsMachineBackupsChecksAndAlerts(t *testing.T) {
	source := twoServers()
	m := start(t, source)
	m, ask := press(t, m, "enter")
	if !m.opened || m.open.name != "web01" {
		t.Fatalf("the server did not open: %+v", m.open)
	}
	if !strings.Contains(screenOf(m), "web01  ONLINE") {
		t.Errorf("the title shows before the report arrives:\n%s", screenOf(m))
	}
	m = deliver(t, m, ask)
	for _, part := range []struct {
		key   string
		wants []string
	}{
		{"", []string{"web01.example.com", "Debian 13", "1 Máy 2", "2 Backup 1", "3 Uptime 1", "4 Alert 1", "Bật được", "1 ngày 1 giờ", "Ổ đĩa /data", "3.0 GiB trống / 100.0 GiB (3% trống)", "Unit lỗi", "cron.service"}},
		{"2", []string{"files", "db", "FAILED: repository is locked", "1 giờ trước"}},
		{"3", []string{"site", "DOWN", "connection refused"}},
		{"4", []string{"disk:/data", "ổ /data gần đầy"}},
	} {
		if part.key != "" {
			m, _ = press(t, m, part.key)
		}
		for _, want := range part.wants {
			if !strings.Contains(screenOf(m), want) {
				t.Errorf("part %q does not show %q:\n%s", part.key, want, screenOf(m))
			}
		}
	}
}

func TestEscLeavesAServerAndQuitsOnlyFromTheList(t *testing.T) {
	m := start(t, twoServers())
	m, _ = press(t, m, "enter")
	m, cmd := press(t, m, "esc")
	if m.opened || cmd != nil {
		t.Fatalf("esc in a server: opened=%v, quits=%v", m.opened, cmd != nil)
	}
	if _, cmd = press(t, m, "q"); cmd == nil {
		t.Fatal("q on the list did not quit")
	}
	if msg := cmd(); msg != (tea.QuitMsg{}) {
		t.Errorf("q gave %T", msg)
	}
}

func TestAServerWithoutAReportSaysSo(t *testing.T) {
	m := start(t, twoServers())
	m, _ = press(t, m, "down")
	m, ask := press(t, m, "enter")
	m = deliver(t, m, ask)
	if !strings.Contains(screenOf(m), "Chưa nhận báo cáo nào từ server này.") {
		t.Errorf("no word for a server that never reported:\n%s", screenOf(m))
	}
}

func TestAReportThatCannotBeReadIsShownAndTheListStays(t *testing.T) {
	source := twoServers()
	m := start(t, source)
	m, ask := press(t, m, "enter")
	source.err = errors.New("connection reset")
	m = deliver(t, m, ask)
	if !strings.Contains(screenOf(m), "Không đọc được server này: connection reset") {
		t.Errorf("no error:\n%s", screenOf(m))
	}
	m, _ = press(t, m, "esc")
	if len(m.servers) != 2 {
		t.Error("the list was lost")
	}
}

func TestAnAnswerForAnotherServerIsDropped(t *testing.T) {
	source := twoServers()
	m := start(t, source)
	m, _ = press(t, m, "enter")
	next, _ := m.Update(detailMsg{name: "db01", detail: ctlapi.AgentDetail{Agent: ctlapi.Agent{Name: "db01"}}})
	if got := next.(model); got.open.read || got.open.detail.Report != nil {
		t.Error("the answer for db01 filled the screen of web01")
	}
	m, _ = press(t, m, "esc")
	next, _ = m.Update(detailMsg{name: "web01", detail: source.details["web01"]})
	if next.(model).open.read {
		t.Error("an answer after leaving the screen was kept")
	}
}

func TestAFailedAskKeepsWhatWasShownAndSaysSo(t *testing.T) {
	source := twoServers()
	m := start(t, source)
	source.err = errors.New("connection refused")
	m.loading = true
	m = deliver(t, m, m.askList())
	if len(m.servers) != 2 {
		t.Fatal("the servers were dropped")
	}
	screen := screenOf(m)
	if !strings.Contains(screen, "không hỏi được control plane") || !strings.Contains(screen, "connection refused") {
		t.Errorf("the failure is not shown:\n%s", screen)
	}
	source.err = nil
	m.loading = true
	m = deliver(t, m, m.askList())
	if strings.Contains(screenOf(m), "không hỏi được") {
		t.Error("the failure stayed after the control plane answered")
	}
}

func TestAnEndedSessionQuitsAndIsReturned(t *testing.T) {
	source := twoServers()
	m := start(t, source)
	source.err = client.ErrSessionEnded
	m.loading = true
	next, cmd := m.Update(m.askList()())
	got := next.(model)
	if !errors.Is(got.fatal, client.ErrSessionEnded) || cmd == nil {
		t.Fatalf("fatal = %v, quits = %v", got.fatal, cmd != nil)
	}
}

func TestAsksDoNotPileUpBehindASlowOne(t *testing.T) {
	source := twoServers()
	m := newModel(context.Background(), source, func() time.Time { return now })
	m.loading = true
	if _, cmd := m.ask(); cmd != nil {
		t.Error("a second ask started while the first had not answered")
	}
	m.loading = false
	if next, cmd := m.ask(); cmd == nil || !next.loading {
		t.Error("an idle screen did not ask")
	}
	m, _ = press(t, start(t, source), "enter")
	m.open.loading = true
	m.loading = true
	if _, cmd := m.ask(); cmd != nil {
		t.Error("a server asked twice")
	}
}

func TestEveryLineFitsTheTerminal(t *testing.T) {
	source := twoServers()
	long := strings.Repeat("long-name-", 10)
	source.details[long] = source.details["web01"]
	source.servers[0].Name = long
	source.alerts[0].Message = strings.Repeat("không báo cáo ", 40)
	for _, width := range []int{1, 2, 5, 10, 16, 20, 30, 60, 100, 200} {
		m := start(t, source)
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		m = next.(model)
		screens := []string{screenOf(m)}
		m, _ = press(t, m, "tab")
		screens = append(screens, screenOf(m))
		m, _ = press(t, m, "tab", "enter")
		m = deliver(t, m, m.askDetail(m.open.name))
		for part := range partCount {
			m.open.current = part
			screens = append(screens, screenOf(m))
		}
		for _, screen := range screens {
			lines := strings.Split(screen, "\n")
			if len(lines) > 24 || len(lines) < 5 {
				t.Errorf("width %d: %d lines for a screen of 24", width, len(lines))
			}
			for _, line := range lines {
				if lipgloss.Width(line) > width {
					t.Errorf("width %d: a line of %d cells: %q", width, lipgloss.Width(line), line)
				}
			}
		}
	}
}

func TestTheSelectedLineStaysInViewInALongList(t *testing.T) {
	source := &fakeSource{}
	for number := range 60 {
		name := "srv" + strings.Repeat("0", 2-len(string(rune('0'+number%10)))) + string(rune('0'+number/10)) + string(rune('0'+number%10))
		source.servers = append(source.servers, ctlapi.Agent{Name: name, Status: "ONLINE", State: "active"})
	}
	m := start(t, source)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = next.(model)
	m, _ = press(t, m, "G")
	if screen := screenOf(m); !strings.Contains(screen, source.servers[59].Name) {
		t.Errorf("the last server is not in view:\n%s", screen)
	}
	m, _ = press(t, m, "g")
	screen := screenOf(m)
	if !strings.Contains(screen, source.servers[0].Name) || !strings.Contains(screen, "… còn") {
		t.Errorf("the top of the list, or the note that more follows, is missing:\n%s", screen)
	}
}

func TestADiskBelowTheLimitIsRedAndAFullOneIsNot(t *testing.T) {
	host := agentapi.HostStatus{MinFreePercent: 10, Disks: []agentapi.DiskStatus{
		{Mount: "/", TotalBytes: 100, FreeBytes: 50}, {Mount: "/data", TotalBytes: 100, FreeBytes: 5}, {Mount: "/empty"},
	}}
	levels := map[string]level{}
	for _, line := range hostTable(host).lines {
		levels[line.cells[0]] = line.level
	}
	if levels["Ổ đĩa /"] != levelNone || levels["Ổ đĩa /data"] != levelBad || levels["Ổ đĩa /empty"] != levelNone {
		t.Errorf("levels = %v", levels)
	}
}

func TestHelpOpensWithAQuestionMarkAndAnyKeyClosesIt(t *testing.T) {
	m := start(t, twoServers())
	m, _ = press(t, m, "?")
	if !strings.Contains(screenOf(m), "luôn hỏi trước") {
		t.Errorf("no help:\n%s", screenOf(m))
	}
	m, cmd := press(t, m, "q")
	if m.help || cmd != nil {
		t.Errorf("a key that closes the help also acted: help=%v quits=%v", m.help, cmd != nil)
	}
}

func TestCtrlCQuitsFromAnywhere(t *testing.T) {
	m := start(t, twoServers())
	m, _ = press(t, m, "enter", "?")
	if _, cmd := press(t, m, "ctrl+c"); cmd == nil {
		t.Error("ctrl+c did not quit")
	}
}

func TestWrapBreaksOnSpacesAndCutsTheLastLine(t *testing.T) {
	for _, tc := range []struct {
		text  string
		width int
		lines int
		want  []string
	}{
		{"một hai ba bốn", 20, 2, []string{"một hai ba bốn"}},
		{"một hai ba bốn", 8, 2, []string{"một hai", "ba bốn"}},
		{"một hai ba bốn năm", 8, 2, []string{"một hai", "ba bốn …"}},
		{strings.Repeat("x", 30), 10, 2, []string{"xxxxxxxxx…"}},
		{"", 10, 2, nil},
	} {
		got := wrap(tc.text, tc.width, tc.lines)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("wrap(%q, %d, %d) = %q, want %q", tc.text, tc.width, tc.lines, got, tc.want)
		}
	}
}

func TestALongSentenceTakesTwoLinesInsteadOfBeingCut(t *testing.T) {
	m := start(t, twoServers())
	next, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	if !strings.Contains(screenOf(next.(model)), "(còn 6") {
		t.Errorf("the end of the sentence is missing:\n%s", screenOf(next.(model)))
	}
}

func TestTheListCursorIsNotMovedByARefreshWhileAServerIsOpen(t *testing.T) {
	m := start(t, twoServers())
	m, _ = press(t, m, "down", "enter") // db01 has no report: its screen has no line
	m = deliver(t, m, m.askList())
	m, _ = press(t, m, "esc")
	if m.cursor != 1 {
		t.Errorf("the cursor of the list is %d after a refresh, want 1", m.cursor)
	}
}

func TestTheFirstAskIsCountedSoTheTimerDoesNotStartASecond(t *testing.T) {
	m := newModel(context.Background(), twoServers(), func() time.Time { return now })
	if _, cmd := m.ask(); cmd != nil {
		t.Error("a second list ask started while the first, from Init, was under way")
	}
}

func TestAnExpiredCertificateIsSaidSo(t *testing.T) {
	server := ctlapi.Agent{Name: "web01", Status: "ONLINE", CertNotAfter: ago(3 * 24 * time.Hour)}
	sentence := serverSentence(server, now)
	if !strings.Contains(sentence, "ĐÃ HẾT HẠN") || strings.Contains(sentence, "còn -") {
		t.Errorf("sentence = %q", sentence)
	}
}

func TestNumbersFromAMachineCannotMakeNonsense(t *testing.T) {
	for _, tc := range []struct {
		free, total uint64
		want        int
	}{{50, 100, 50}, {0, 100, 0}, {200, 100, 100}, {1 << 63, 1 << 63, 100}, {1 << 62, 1<<63 + 1, 50}, {5, 0, 100}} {
		if got := freePercent(tc.free, tc.total); got != tc.want {
			t.Errorf("freePercent(%d, %d) = %d, want %d", tc.free, tc.total, got, tc.want)
		}
	}
}

func TestATinyTerminalGetsAtMostItsHeight(t *testing.T) {
	m := start(t, twoServers())
	for height := range 4 {
		next, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: height})
		lines := strings.Count(screenOf(next.(model)), "\n") + 1
		if height > 0 && lines > height {
			t.Errorf("height %d drew %d lines", height, lines)
		}
	}
}
