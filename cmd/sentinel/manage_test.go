package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

var escapeCodes = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain drops the colour codes so tests read the text a user sees.
func plain(view string) string { return escapeCodes.ReplaceAllString(view, "") }

func sampleReport() statusReport {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	return statusReport{
		Server: "web-01", GeneratedAt: now,
		Alerts: []alertEntry{{Key: "backup:appdb:s3", Message: "appdb has not backed up for 3 days", Since: now.Add(-time.Hour)}},
		Backups: []backupEntry{
			{Job: "appdb", Repository: "s3", FinishedAt: now.Add(-time.Hour), Success: true, SnapshotID: "0123456789abcdef", BytesAdded: 2048, Result: "ok"},
			{Job: "files", Repository: "local", FinishedAt: now.Add(-2 * time.Hour), Success: false, Error: "restic exited 1", Result: "FAILED: restic exited 1"},
			{Job: "mongo", Repository: "s3", FinishedAt: now.Add(-3 * time.Hour), Success: true, SnapshotID: "fedcba9876543210", Warning: "forget failed", Result: "ok (warning: forget failed)"},
		},
		Checks: []checkEntry{{Name: "site", OK: true, LatencyMs: 42, At: now}, {Name: "api", OK: false, At: now, Error: "connection refused"}},
	}
}

func loadedModel(t *testing.T) manageModel {
	t.Helper()
	model, _ := newManageModel(context.Background(), func() (statusReport, error) { return sampleReport(), nil }, manageActions{}, nil).Update(reportMsg{report: sampleReport()})
	return model.(manageModel)
}

func press(t *testing.T, screen manageModel, key string) (manageModel, tea.Cmd) {
	t.Helper()
	model, cmd := screen.onKey(key, "")
	return model.(manageModel), cmd
}

func TestManageBackupsTab(t *testing.T) {
	view := plain(loadedModel(t).render())
	for _, want := range []string{"web-01", "1 Backups (3)", "appdb", "01234567", "2.0 KiB", "FAILED", "warning", "3 Alerts (1)"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "fedcba9876543210") {
		t.Error("snapshot ids must be shortened")
	}
}

func TestManageKeysSwitchTabsAndMoveSelection(t *testing.T) {
	screen := loadedModel(t)
	screen, _ = press(t, screen, "down")
	if screen.cursor != 1 || !strings.Contains(plain(screen.render()), "files → local: FAILED: restic exited 1") {
		t.Fatalf("cursor %d; detail of the selected line missing:\n%s", screen.cursor, screen.render())
	}
	screen, _ = press(t, screen, "2")
	if screen.tab != 1 || screen.cursor != 0 {
		t.Fatalf("tab %d cursor %d, want tab 1 cursor 0", screen.tab, screen.cursor)
	}
	if view := plain(screen.render()); !strings.Contains(view, "DOWN") || !strings.Contains(view, "site") {
		t.Errorf("uptime tab:\n%s", view)
	}
	screen, _ = press(t, screen, "tab")
	screen, _ = press(t, screen, "tab")
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
	model, _ := newManageModel(context.Background(), func() (statusReport, error) { return statusReport{}, nil }, manageActions{}, nil).Update(reportMsg{})
	if view := plain(model.(manageModel).render()); !strings.Contains(view, "No backups recorded yet") {
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

func TestPrintStatusAndJSONReport(t *testing.T) {
	var out strings.Builder
	if err := printStatus(&out, sampleReport()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Server: web-01", "Open alerts: 1", "appdb", "01234567", "FAILED: restic exited 1", "DOWN: connection refused"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
}
