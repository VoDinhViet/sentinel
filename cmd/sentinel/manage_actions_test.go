package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/restic"
	"sentinel/internal/status"
)

// messagesOf runs a command and returns what it produced; batches are flattened
// and the spinner ticks are dropped.
func messagesOf(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var all []tea.Msg
		for _, inner := range batch {
			all = append(all, messagesOf(inner)...)
		}
		return all
	}
	if _, spinner := msg.(spinMsg); spinner {
		return nil
	}
	return []tea.Msg{msg}
}

func feed(t *testing.T, screen manageModel, messages []tea.Msg) manageModel {
	t.Helper()
	for _, msg := range messages {
		if _, isReload := msg.(reportMsg); isReload {
			continue
		}
		model, _ := screen.Update(msg)
		screen = model.(manageModel)
	}
	return screen
}

func typeText(t *testing.T, screen manageModel, text string) manageModel {
	t.Helper()
	for _, char := range text {
		model, _ := screen.onKey(string(char), string(char))
		screen = model.(manageModel)
	}
	return screen
}

type fakeActions struct {
	backups   []string
	stopped   []string
	restored  []string
	restoreTo string
	failWith  error
}

func (f *fakeActions) actions() manageActions {
	return manageActions{
		startBackup: func(job, repository string) (int, string, error) {
			f.backups = append(f.backups, job+"/"+repository)
			if f.failWith != nil {
				return 0, "", f.failWith
			}
			return 4242, "/state/logs/backup-" + job + ".log", nil
		},
		stopBackup: func(job string) error {
			f.stopped = append(f.stopped, job)
			return f.failWith
		},
		snapshots: func(context.Context, string, string) ([]restic.Snapshot, error) {
			if f.failWith != nil {
				return nil, f.failWith
			}
			return []restic.Snapshot{
				{ID: "newest0000000000", Time: time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)},
				{ID: "older00000000000", Time: time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)},
			}, nil
		},
		restore: func(_ context.Context, job, repository string, snapshot restic.Snapshot, target string) (string, error) {
			f.restored = append(f.restored, job+"/"+repository+"/"+snapshot.ID)
			f.restoreTo = target
			if f.failWith != nil {
				return "", f.failWith
			}
			return "pg_restore -d appdb " + target + "/appdb.dump", nil
		},
	}
}

func modelWith(t *testing.T, fake *fakeActions) manageModel {
	t.Helper()
	screen := newManageModel(context.Background(), func() (status.Report, error) { return sampleReport(), nil }, fake.actions(),
		[]jobRepository{{"appdb", "s3"}, {"newjob", "local"}})
	model, _ := screen.Update(reportMsg{report: sampleReport()})
	return model.(manageModel)
}

func TestBackupNowStartsADetachedBackupWithoutBlockingTheScreen(t *testing.T) {
	fake := &fakeActions{}
	screen := modelWith(t, fake)
	model, cmd := screen.onKey("b", "")
	screen = model.(manageModel)
	if screen.mode != modeTable {
		t.Fatalf("b must not switch to the busy screen (mode %d)", screen.mode)
	}
	screen = feed(t, screen, messagesOf(cmd))
	if len(fake.backups) != 1 || fake.backups[0] != "appdb/s3" {
		t.Fatalf("backups started: %v", fake.backups)
	}
	if view := plain(screen.render()); !strings.Contains(view, "đã chạy nền (pid 4242)") || !strings.Contains(view, "backup-appdb.log") {
		t.Errorf("outcome missing:\n%s", view)
	}
}

func TestBackupStartFailureIsShownNotHidden(t *testing.T) {
	fake := &fakeActions{failWith: errors.New("already running")}
	screen := modelWith(t, fake)
	_, cmd := screen.onKey("b", "")
	screen = feed(t, screen, messagesOf(cmd))
	if !screen.noticeBad || !strings.Contains(plain(screen.render()), "already running") {
		t.Errorf("want the failure on screen:\n%s", plain(screen.render()))
	}
}

func runningReport() status.Report {
	report := sampleReport()
	percent := 37.0
	report.Running = []status.Running{{Job: "appdb", Repository: "s3", Percent: &percent, Estimated: true, BytesDone: 7 << 30, BytesTotal: 20 << 30, StartedAt: time.Now().Add(-40 * time.Minute)}}
	return report
}

func TestRunningBackupShowsProgressAndCannotBeStartedTwice(t *testing.T) {
	fake := &fakeActions{}
	screen := modelWith(t, fake)
	model, _ := screen.Update(reportMsg{report: runningReport()})
	screen = model.(manageModel)
	if view := plain(screen.render()); !strings.Contains(view, "⏳ ~37%") || !strings.Contains(view, "ước lượng") {
		t.Fatalf("progress missing:\n%s", view)
	}
	_, cmd := screen.onKey("b", "")
	if cmd != nil || len(fake.backups) != 0 {
		t.Error("b on a running backup must not start another")
	}
	if screen.scheduleRefresh() == nil {
		t.Error("a refresh must be scheduled while a backup runs")
	}
}

func TestStopNeedsTwoPresses(t *testing.T) {
	fake := &fakeActions{}
	screen := modelWith(t, fake)
	model, _ := screen.Update(reportMsg{report: runningReport()})
	model, cmd := model.(manageModel).onKey("x", "")
	if cmd != nil || len(fake.stopped) != 0 || !strings.Contains(model.(manageModel).notice, "x lần nữa") {
		t.Fatalf("first x must only ask (stopped %v, notice %q)", fake.stopped, model.(manageModel).notice)
	}
	model, cmd = model.(manageModel).onKey("x", "")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	if len(fake.stopped) != 1 || fake.stopped[0] != "appdb" || !strings.Contains(screen.notice, "yêu cầu dừng") {
		t.Errorf("second x must stop (stopped %v, notice %q)", fake.stopped, screen.notice)
	}
}

func TestAnotherKeyCancelsTheStopConfirmation(t *testing.T) {
	fake := &fakeActions{}
	screen := modelWith(t, fake)
	model, _ := screen.Update(reportMsg{report: runningReport()})
	model, _ = model.(manageModel).onKey("x", "")
	model, _ = model.(manageModel).onKey("down", "")
	model, _ = model.(manageModel).onKey("up", "")
	_, cmd := model.(manageModel).onKey("x", "")
	if cmd != nil || len(fake.stopped) != 0 {
		t.Error("x after another key must ask again, not stop")
	}
}

func TestBackupKeyOnlyActsOnTheBackupsTab(t *testing.T) {
	fake := &fakeActions{}
	screen := modelWith(t, fake)
	model, _ := screen.onKey("2", "")
	model, cmd := model.(manageModel).onKey("b", "")
	if cmd != nil || model.(manageModel).mode != modeTable || len(fake.backups) != 0 {
		t.Error("b on the Uptime tab must do nothing")
	}
}

func TestJobsThatNeverRanAreListed(t *testing.T) {
	screen := modelWith(t, &fakeActions{})
	view := plain(screen.render())
	if !strings.Contains(view, "newjob") || !strings.Contains(view, "chưa chạy") || !strings.Contains(view, "1 Backups 4") {
		t.Errorf("never-run job missing:\n%s", view)
	}
}

func TestRestoreNeedsTargetAndTheJobNameThenShowsTheLoadCommand(t *testing.T) {
	fake := &fakeActions{}
	screen := modelWith(t, fake)
	model, cmd := screen.onKey("s", "")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	if screen.mode != modeSnapshots || !strings.Contains(plain(screen.render()), "newest00") {
		t.Fatalf("snapshot list missing (mode %d):\n%s", screen.mode, plain(screen.render()))
	}
	model, _ = screen.onKey("down", "")
	model, _ = model.(manageModel).onKey("enter", "")
	screen = model.(manageModel)
	if screen.mode != modeTarget || screen.chosen.ID != "older00000000000" {
		t.Fatalf("mode %d chosen %q", screen.mode, screen.chosen.ID)
	}

	model, _ = screen.onKey("enter", "") // empty target
	screen = model.(manageModel)
	if screen.mode != modeTarget || screen.inputError == "" {
		t.Fatal("an empty or relative target must be refused")
	}
	screen = typeText(t, screen, "relative/dir")
	model, _ = screen.onKey("enter", "")
	if model.(manageModel).mode != modeTarget {
		t.Fatal("a relative target must be refused")
	}
	screen = model.(manageModel)
	for range "relative/dir" {
		model, _ = screen.onKey("backspace", "")
		screen = model.(manageModel)
	}
	screen = typeText(t, screen, "/tmp/restore")
	model, _ = screen.onKey("enter", "")
	screen = model.(manageModel)
	if screen.mode != modeConfirm || !strings.Contains(plain(screen.render()), "/tmp/restore") {
		t.Fatalf("confirm screen missing (mode %d):\n%s", screen.mode, plain(screen.render()))
	}

	screen = typeText(t, screen, "wrong")
	model, cmd = screen.onKey("enter", "")
	screen = model.(manageModel)
	if cmd != nil || len(fake.restored) != 0 || screen.inputError == "" {
		t.Fatal("a wrong job name must not start the restore")
	}
	for range "wrong" {
		model, _ = screen.onKey("backspace", "")
		screen = model.(manageModel)
	}
	screen = typeText(t, screen, "appdb")
	model, cmd = screen.onKey("enter", "")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	if len(fake.restored) != 1 || fake.restored[0] != "appdb/s3/older00000000000" || fake.restoreTo != "/tmp/restore" {
		t.Fatalf("restore call: %v to %q", fake.restored, fake.restoreTo)
	}
	view := plain(screen.render())
	if screen.mode != modeResult || !strings.Contains(view, "Đã restore older000") || !strings.Contains(view, "pg_restore -d appdb /tmp/restore/appdb.dump") {
		t.Errorf("result screen (mode %d):\n%s", screen.mode, view)
	}
	model, _ = screen.onKey("x", "x")
	if model.(manageModel).mode != modeSnapshots {
		t.Error("a key after a successful restore returns to the snapshot list")
	}
}

func TestRestoreFailureIsShown(t *testing.T) {
	fake := &fakeActions{}
	screen := modelWith(t, fake)
	model, cmd := screen.onKey("s", "")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	fake.failWith = errors.New("disk full")
	model, _ = screen.onKey("enter", "")
	screen = typeText(t, model.(manageModel), "/tmp/r")
	model, _ = screen.onKey("enter", "")
	screen = typeText(t, model.(manageModel), "appdb")
	model, cmd = screen.onKey("enter", "")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	if !screen.resultBad || !strings.Contains(plain(screen.render()), "Restore thất bại: disk full") {
		t.Errorf("failure missing:\n%s", plain(screen.render()))
	}
}

func TestRestoreToRootWarns(t *testing.T) {
	screen := modelWith(t, &fakeActions{})
	model, cmd := screen.onKey("s", "")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	model, _ = screen.onKey("enter", "")
	screen = typeText(t, model.(manageModel), "/")
	model, _ = screen.onKey("enter", "")
	if view := plain(model.(manageModel).render()); !strings.Contains(view, "restore tại chỗ") {
		t.Errorf("want a warning for /:\n%s", view)
	}
}

func TestEscapeBacksOutOfRestore(t *testing.T) {
	screen := modelWith(t, &fakeActions{})
	model, cmd := screen.onKey("s", "")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	model, _ = screen.onKey("enter", "")
	model, _ = model.(manageModel).onKey("esc", "")
	if model.(manageModel).mode != modeSnapshots {
		t.Error("esc in the target prompt returns to the snapshot list")
	}
	model, _ = model.(manageModel).onKey("esc", "")
	if model.(manageModel).mode != modeTable {
		t.Error("esc in the snapshot list returns to the tabs")
	}
}

func TestCtrlCCancelsARunningActionButQDoesNotQuit(t *testing.T) {
	screen := modelWith(t, &fakeActions{})
	ctx, cancel := context.WithCancel(context.Background())
	screen.mode, screen.cancelBusy = modeBusy, cancel
	model, cmd := screen.onKey("q", "q")
	if cmd != nil || model.(manageModel).quitting {
		t.Error("q must not quit while an action runs")
	}
	model, _ = model.(manageModel).onKey("ctrl+c", "")
	if ctx.Err() == nil || !strings.Contains(model.(manageModel).busyText, "đang huỷ") {
		t.Error("ctrl+c must cancel the running action")
	}
}
