package tui

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"sentinel/agent/internal/status"
)

var escapeCodes = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain drops the colour codes so tests read the text a user sees.
func plain(view string) string { return escapeCodes.ReplaceAllString(view, "") }

func sampleReport() status.Report {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	return status.Report{
		Server: "web-01", GeneratedAt: now,
		Alerts: []status.Alert{{Key: "backup:appdb:s3", Message: "appdb has not backed up for 3 days", Since: now.Add(-time.Hour)}},
		Jobs: []status.Job{
			{Job: "appdb", Repository: "s3", FinishedAt: now.Add(-time.Hour), Success: true, SnapshotID: "0123456789abcdef", BytesAdded: 2048, Result: "ok", Recent: []string{"ok", "ok", "failed", "ok"}},
			{Job: "files", Repository: "local", FinishedAt: now.Add(-2 * time.Hour), Success: false, Error: "restic exited 1", Result: "FAILED: restic exited 1", Recent: []string{"ok", "failed", "failed"}},
			{Job: "mongo", Repository: "s3", FinishedAt: now.Add(-3 * time.Hour), Success: true, SnapshotID: "fedcba9876543210", Warning: "forget failed", Result: "ok (warning: forget failed)"},
		},
		Checks: []status.Check{{Name: "site", OK: true, LatencyMs: 42, At: now}, {Name: "api", OK: false, At: now, Error: "connection refused"}},
	}
}

func loadedModel(t *testing.T) manageModel {
	t.Helper()
	model, _ := newManageModel(context.Background(), func() (status.Report, error) { return sampleReport(), nil }, manageActions{}, nil).Update(reportMsg{report: sampleReport()})
	return model.(manageModel)
}

func press(t *testing.T, screen manageModel, key string) (manageModel, tea.Cmd) {
	t.Helper()
	model, cmd := screen.onKey(key, "")
	return model.(manageModel), cmd
}

func TestManageBackupsTab(t *testing.T) {
	view := plain(loadedModel(t).render())
	for _, want := range []string{"web-01", "1 Backups 3", "appdb", "01234567", "2.0 KiB", "LỖI", "cảnh báo", "3 Alerts 1"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "fedcba9876543210") {
		t.Error("snapshot ids must be shortened")
	}
}

func TestManageSummaryAndHistoryGlyphs(t *testing.T) {
	view := plain(loadedModel(t).render())
	for _, want := range []string{"1 lỗi · 1 alert · 1 check down · 1 cảnh báo · 1 ổn", "✓✓✗✓", "✓✗✗", "1 giờ trước", "2 giờ trước"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "2026-11-23") {
		t.Error("times on the screen are relative")
	}
}

func TestManageSummaryAllGoodAndNarrow(t *testing.T) {
	healthy := status.Report{Server: "s", GeneratedAt: time.Now(), Jobs: []status.Job{{Job: "a", Repository: "r", Success: true, Result: "ok"}}}
	model, _ := newManageModel(context.Background(), func() (status.Report, error) { return healthy, nil }, manageActions{}, nil).Update(reportMsg{report: healthy})
	if got := plain(model.(manageModel).summary()); got != "1 ổn" {
		t.Errorf("summary = %q", got)
	}
	narrow := loadedModel(t)
	narrow.width = 20
	if got := plain(narrow.summary()); len([]rune(got)) > 20 || !strings.HasPrefix(got, "1 lỗi") {
		t.Errorf("narrow summary = %q (the most important parts stay)", got)
	}
	empty, _ := newManageModel(context.Background(), func() (status.Report, error) { return status.Report{}, nil }, manageActions{}, nil).Update(reportMsg{})
	if got := plain(empty.(manageModel).summary()); got != "chưa cấu hình gì" {
		t.Errorf("empty summary = %q", got)
	}
}

func TestManageKeysSwitchTabsAndMoveSelection(t *testing.T) {
	screen := loadedModel(t)
	screen, _ = press(t, screen, "down")
	view := plain(screen.render())
	if screen.cursor != 1 || !strings.Contains(view, "files → local") || !strings.Contains(view, "FAILED: restic exited 1") {
		t.Fatalf("cursor %d; detail of the selected line missing:\n%s", screen.cursor, view)
	}
	screen, _ = press(t, screen, "2")
	if screen.tab != 1 || screen.cursor != 0 {
		t.Fatalf("tab %d cursor %d, want tab 1 cursor 0", screen.tab, screen.cursor)
	}
	if view := plain(screen.render()); !strings.Contains(view, "DOWN") || !strings.Contains(view, "site") {
		t.Errorf("uptime tab:\n%s", view)
	}
	for range len(screen.tabs()) - 1 {
		screen, _ = press(t, screen, "tab")
	}
	if screen.tab != 0 {
		t.Errorf("tab wraps around, got %d", screen.tab)
	}
	screen, _ = press(t, screen, "G")
	if screen.cursor != 2 {
		t.Errorf("G goes to the last line, cursor %d", screen.cursor)
	}
	screen, _ = press(t, screen, "down")
	if screen.cursor != 2 {
		t.Errorf("cursor must stop at the last line, got %d", screen.cursor)
	}
}

func TestManageQuit(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c", "esc"} {
		screen, cmd := press(t, loadedModel(t), key)
		if cmd == nil || !screen.quitting {
			t.Errorf("%s must quit", key)
		}
	}
}

func TestManageShowsStoreErrorAndKeepsLastReport(t *testing.T) {
	screen := loadedModel(t)
	model, _ := screen.Update(reportMsg{err: errors.New("database is locked")})
	view := plain(model.(manageModel).render())
	if !strings.Contains(view, "database is locked") || !strings.Contains(view, "appdb") {
		t.Errorf("want the error and the previous data:\n%s", view)
	}
}

func TestManageEmptyAndNarrow(t *testing.T) {
	model, _ := newManageModel(context.Background(), func() (status.Report, error) { return status.Report{}, nil }, manageActions{}, nil).Update(reportMsg{})
	if view := plain(model.(manageModel).render()); !strings.Contains(view, "Chưa có backup nào") {
		t.Errorf("empty view:\n%s", view)
	}
	narrow := loadedModel(t)
	model, _ = narrow.Update(tea.WindowSizeMsg{Width: 44, Height: 14})
	for _, line := range strings.Split(plain(model.(manageModel).render()), "\n") {
		if width := len([]rune(line)); width > 44 {
			t.Errorf("line too long for a 44-column screen (%d): %q", width, line)
		}
	}
}

func TestWideScreenShowsPanelsAndPinsTheKeyBarToTheBottom(t *testing.T) {
	model, _ := loadedModel(t).Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	screen := model.(manageModel)
	lines := strings.Split(plain(screen.render()), "\n")
	if len(lines) != 30 {
		t.Fatalf("the screen is filled: %d lines, want 30", len(lines))
	}
	for i, line := range lines {
		if width := len([]rune(line)); width > 120 {
			t.Errorf("line %d is %d cells wide on a 120-column screen: %q", i, width, line)
		}
	}
	view := strings.Join(lines, "\n")
	for _, want := range []string{"◆ Sentinel", "tổng quan", "đang chạy", "uptime", "Jobs", "Alerts", "Repos"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if !strings.Contains(lines[len(lines)-1], "q thoát") {
		t.Errorf("the key bar is the last line: %q", lines[len(lines)-1])
	}
	model, _ = screen.Update(tea.WindowSizeMsg{Width: 90, Height: 24})
	narrow := plain(model.(manageModel).render())
	if strings.Contains(narrow, "tổng quan") {
		t.Errorf("below %d columns the panels give way to the summary line:\n%s", topPanelsMinWidth, narrow)
	}
}

// longTextModel is a screen whose texts are far wider than any terminal.
func longTextModel(t *testing.T) manageModel {
	t.Helper()
	long := strings.Repeat("/very/long/path/", 30)
	report := sampleReport()
	for i := range report.Jobs {
		if !report.Jobs[i].Success {
			report.Jobs[i].Error, report.Jobs[i].Result = "restic exited 1: "+long, "FAILED: restic exited 1: "+long
		}
	}
	report.Checks[1].Error = "dial tcp: " + long
	model, _ := newManageModel(context.Background(), func() (status.Report, error) { return report, nil }, manageActions{}, nil).Update(reportMsg{report: report})
	screen := model.(manageModel)
	screen.notice, screen.noticeBad = "Đã chạy nền. Log: "+long, false
	return screen
}

func TestEveryScreenFitsTheTerminalWhateverTheTextLength(t *testing.T) {
	for _, size := range [][2]int{{44, 14}, {60, 20}, {80, 24}, {100, 30}, {130, 34}, {160, 40}} {
		width, height := size[0], size[1]
		model, _ := longTextModel(t).Update(tea.WindowSizeMsg{Width: width, Height: height})
		screen := model.(manageModel)
		for tab := range len(screen.tabs()) {
			screen.tab, screen.cursor = tab, 1
			lines := strings.Split(plain(screen.render()), "\n")
			if len(lines) != height {
				t.Errorf("%dx%d tab %d: %d lines, want %d", width, height, tab, len(lines), height)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > width {
					t.Errorf("%dx%d tab %d: line %d is %d cells wide: %q", width, height, tab, i, got, line)
				}
			}
		}
	}
}

func TestOtherScreensPinTheKeyBarToTheBottomAndItsKeysStillClick(t *testing.T) {
	formScreen, _ := formModel(t)
	for name, start := range map[string]struct {
		screen manageModel
		key    string
	}{
		"help":     {loadedModel(t), "?"},
		"add form": {formScreen, "a"},
	} {
		model, _ := start.screen.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		screen, _ := press(t, model.(manageModel), start.key)
		lines := strings.Split(plain(screen.render()), "\n")
		if len(lines) != 40 {
			t.Fatalf("%s: %d lines, want 40", name, len(lines))
		}
		if bar, rule := lines[len(lines)-2], lines[len(lines)-3]; !strings.Contains(rule, "────") || strings.TrimSpace(bar) == "" {
			t.Errorf("%s: the key bar must sit under a rule on the last row inside the frame:\n%s", name, strings.Join(lines[len(lines)-4:], "\n"))
		}
	}
	model, _ := loadedModel(t).Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	screen, _ := press(t, model.(manageModel), "?")
	if row := screenRow(screen, targetFor(t, screen, "esc").row); !strings.Contains(row, "đóng") {
		t.Errorf("the click target of the help's key bar is not on the key bar: %q", row)
	}
}

func TestTheOpenTabAndTheCountsFollowTheReport(t *testing.T) {
	screen := loadedModel(t)
	if title, _ := screen.tabTitle(screen.tabs(), 100); !strings.Contains(plain(title), "1 Backups 3") || !strings.Contains(plain(title), "3 Alerts 1") {
		t.Errorf("tab title: %q", plain(title))
	}
	if tabs := screen.tabs(); !tabs[backupsTab].alarm || !tabs[uptimeTab].alarm || !tabs[alertsTab].alarm {
		t.Errorf("a tab with a failed job, a check down or an open alert is red: %+v", []bool{tabs[backupsTab].alarm, tabs[uptimeTab].alarm, tabs[alertsTab].alarm})
	}
	if view := plain(screen.render()); !strings.Contains(view, "└ restic exited 1") {
		t.Errorf("a failed job shows its error under its row:\n%s", view)
	}
}
