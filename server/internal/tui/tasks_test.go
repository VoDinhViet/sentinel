package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"errors"
	"strings"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/client"
	"sentinel/server/internal/ctlapi"
)

var errSessionOver = client.ErrSessionEnded

// openBackups is the Backup part of web01, with the cursor on its first job (files into s3).
func openBackups(t *testing.T, source *fakeSource) model {
	t.Helper()
	m := start(t, source)
	m, ask := press(t, m, "enter")
	m = deliver(t, m, ask)
	m, _ = press(t, m, "2")
	return m
}

func TestBAsksFirstAndSendsOnlyAfterY(t *testing.T) {
	source := twoServers()
	m := openBackups(t, source)
	m, ask := press(t, m, "b")
	if ask != nil || len(source.given) != 0 {
		t.Fatalf("b sent a task before the person said yes: %+v", source.given)
	}
	screen := screenOf(m)
	if !strings.Contains(screen, "Chạy backup job files vào repository s3 trên web01?") || !strings.Contains(screen, "y: đồng ý") {
		t.Fatalf("no question on the screen:\n%s", screen)
	}
	m, ask = press(t, m, "y")
	if ask == nil {
		t.Fatal("y sent nothing")
	}
	m = deliver(t, m, ask)
	if len(source.given) != 1 || source.given[0].agent != "web01" || source.given[0].action != agentapi.ActionBackupStart ||
		source.given[0].params["job"] != "files" || source.given[0].params["repository"] != "s3" {
		t.Fatalf("given = %+v", source.given)
	}
	if !strings.Contains(screenOf(m), "Đã giao việc #1 (backup.start)") {
		t.Errorf("the person is not told it was given:\n%s", screenOf(m))
	}
}

func TestAnyOtherKeyThanYDropsTheQuestion(t *testing.T) {
	for _, key := range []string{"n", "esc", "enter", "q"} {
		source := twoServers()
		m := openBackups(t, source)
		m, _ = press(t, m, "s")
		if !strings.Contains(screenOf(m), "Dừng backup job files trên web01?") {
			t.Fatalf("no question:\n%s", screenOf(m))
		}
		m, ask := press(t, m, key)
		if ask != nil || len(source.given) != 0 || !m.opened || m.open.confirm != nil {
			t.Errorf("%q: sent %+v, still open %v, still asking %v", key, source.given, m.opened, m.open.confirm != nil)
		}
	}
}

func TestSStopsTheBackupOfTheJobUnderTheCursor(t *testing.T) {
	source := twoServers()
	m := openBackups(t, source)
	m, _ = press(t, m, "down", "s")
	m, ask := press(t, m, "y")
	deliver(t, m, ask)
	if len(source.given) != 1 || source.given[0].action != agentapi.ActionBackupStop || source.given[0].params["job"] != "db" || len(source.given[0].params) != 1 {
		t.Errorf("given = %+v", source.given)
	}
}

func TestBAndSDoNothingOutsideTheBackupPart(t *testing.T) {
	source := twoServers()
	m := start(t, source)
	m, ask := press(t, m, "enter")
	m = deliver(t, m, ask)
	for _, part := range []string{"1", "3", "4", "5"} {
		m, _ = press(t, m, part, "b", "s")
		if m.open.confirm != nil {
			t.Errorf("part %s asked a question", part)
		}
	}
}

func TestAServerThatDoesNotAllowBackupsIsToldSoBeforeAnythingIsSent(t *testing.T) {
	source := twoServers()
	report := source.details["web01"].Report
	report.Allow = []string{}
	m := openBackups(t, source)
	m, ask := press(t, m, "b")
	screen := screenOf(m)
	if ask != nil || m.open.confirm != nil || !strings.Contains(screen, "chưa cho phép backup.start") || !strings.Contains(screen, "control_plane.allow.backup") {
		t.Errorf("confirm %v, screen:\n%s", m.open.confirm, screen)
	}
}

func TestATaskThatCannotBeGivenIsShownAsAnError(t *testing.T) {
	source := twoServers()
	source.createErr = errors.New("this server already has 20 open tasks")
	m := openBackups(t, source)
	m, _ = press(t, m, "b")
	m, ask := press(t, m, "y")
	m = deliver(t, m, ask)
	if !strings.Contains(screenOf(m), "Không giao được việc: this server already has 20 open tasks") {
		t.Errorf("screen:\n%s", screenOf(m))
	}
}

func TestAnEndedSessionWhileGivingATaskQuits(t *testing.T) {
	source := twoServers()
	source.createErr = errors.New("x")
	m := openBackups(t, source)
	m, _ = press(t, m, "b")
	m, ask := press(t, m, "y")
	source.createErr = errSessionOver
	next, quit := m.Update(ask())
	if quit == nil || next.(model).fatal == nil {
		t.Errorf("the screen stayed after the session ended: %+v", next.(model).fatal)
	}
}

func TestThePartOfTasksListsTheLatestNewestFirstWithHowEachEnded(t *testing.T) {
	source := twoServers()
	source.tasks = []ctlapi.Task{
		{ID: 3, Action: agentapi.ActionBackupStart, Params: map[string]string{"job": "files", "repository": "s3"}, State: agentapi.TaskRefused, CreatedAt: *ago(time.Minute), Error: "backup.start is not allowed: set control_plane.allow.backup"},
		{ID: 2, Action: agentapi.ActionDoctorRun, State: agentapi.TaskSucceeded, CreatedAt: *ago(time.Hour), Output: "ok  config"},
	}
	m := start(t, source)
	m, ask := press(t, m, "enter")
	m = deliver(t, m, ask)
	m, _ = press(t, m, "5")
	screen := screenOf(m)
	for _, want := range []string{"Việc", "backup.start", "job=files repository=s3", "refused", "doctor.run", "succeeded", "not allowed"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the tasks do not show %q:\n%s", want, screen)
		}
	}
}

func TestAServerWithNoTasksSaysHowToGiveOne(t *testing.T) {
	m := start(t, twoServers())
	m, ask := press(t, m, "enter")
	m = deliver(t, m, ask)
	m, _ = press(t, m, "5")
	if !strings.Contains(screenOf(m), "Chưa giao việc nào") {
		t.Errorf("screen:\n%s", screenOf(m))
	}
}

func TestTheKeyBarOffersBAndSOnlyOnTheBackupPart(t *testing.T) {
	m := openBackups(t, twoServers())
	if !strings.Contains(screenOf(m), "chạy backup") {
		t.Errorf("no b on the Backup part:\n%s", screenOf(m))
	}
	m, _ = press(t, m, "1")
	if strings.Contains(screenOf(m), "chạy backup") {
		t.Errorf("b is offered on the Máy part:\n%s", screenOf(m))
	}
}

func TestTheTasksTheQuestionAndTheNoticeFitTheTerminal(t *testing.T) {
	source := twoServers()
	source.tasks = []ctlapi.Task{{ID: 1, Action: agentapi.ActionBackupStart, Params: map[string]string{"job": strings.Repeat("j", 80), "repository": "s3"},
		State: agentapi.TaskFailed, CreatedAt: *ago(time.Minute), Error: strings.Repeat("rất dài ", 60)}}
	source.createErr = errors.New(strings.Repeat("lỗi dài ", 40))
	for _, width := range []int{1, 5, 16, 30, 60, 100} {
		m := openBackups(t, source)
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 20})
		m = next.(model)
		screens := []string{}
		m, _ = press(t, m, "b")
		screens = append(screens, screenOf(m))
		m, ask := press(t, m, "y")
		m = deliver(t, m, ask)
		screens = append(screens, screenOf(m))
		m, _ = press(t, m, "5")
		screens = append(screens, screenOf(m))
		for _, screen := range screens {
			lines := strings.Split(screen, "\n")
			if len(lines) > 20 {
				t.Errorf("width %d: %d lines for a screen of 20", width, len(lines))
			}
			for _, line := range lines {
				if lipgloss.Width(line) > width {
					t.Errorf("width %d: a line of %d cells: %q", width, lipgloss.Width(line), line)
				}
			}
		}
	}
}
