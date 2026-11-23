package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/backup"
	"sentinel/internal/store"
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

func feed(t *testing.T, d manager, messages []tea.Msg) manager {
	t.Helper()
	for _, msg := range messages {
		if _, isReload := msg.(reportMsg); isReload {
			continue
		}
		model, _ := d.Update(msg)
		d = model.(manager)
	}
	return d
}

func typeText(t *testing.T, d manager, text string) manager {
	t.Helper()
	for _, r := range text {
		model, _ := d.onKey(string(r), string(r))
		d = model.(manager)
	}
	return d
}

type fakeActions struct {
	backups   []string
	restored  []string
	restoreTo string
	failWith  error
}

func (f *fakeActions) actions() manageActions {
	return manageActions{
		runBackup: func(_ context.Context, job, repository string) ([]store.BackupRun, error) {
			f.backups = append(f.backups, job+"/"+repository)
			if f.failWith != nil {
				return nil, f.failWith
			}
			return []store.BackupRun{{Job: job, Repository: repository, Success: true, SnapshotID: "aabbccddeeff0011"}}, nil
		},
		snapshots: func(context.Context, string, string) ([]backup.Snapshot, error) {
			if f.failWith != nil {
				return nil, f.failWith
			}
			return []backup.Snapshot{
				{ID: "newest0000000000", Time: time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)},
				{ID: "older00000000000", Time: time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)},
			}, nil
		},
		restore: func(_ context.Context, job, repository string, snapshot backup.Snapshot, target string) (string, error) {
			f.restored = append(f.restored, job+"/"+repository+"/"+snapshot.ID)
			f.restoreTo = target
			if f.failWith != nil {
				return "", f.failWith
			}
			return "pg_restore -d appdb " + target + "/appdb.dump", nil
		},
	}
}

func managerWith(t *testing.T, fake *fakeActions) manager {
	t.Helper()
	d := newManager(context.Background(), func() (statusReport, error) { return sampleReport(), nil }, fake.actions(),
		[]jobRepository{{"appdb", "s3"}, {"newjob", "local"}})
	model, _ := d.Update(reportMsg{report: sampleReport()})
	return model.(manager)
}

func TestBackupNowRunsTheSelectedJobAndShowsTheOutcome(t *testing.T) {
	fake := &fakeActions{}
	d := managerWith(t, fake)
	model, cmd := d.onKey("b", "")
	d = model.(manager)
	if d.mode != modeBusy || !strings.Contains(plain(d.render()), "Backing up appdb → s3") {
		t.Fatalf("busy screen missing:\n%s", plain(d.render()))
	}
	d = feed(t, d, messagesOf(cmd))
	if len(fake.backups) != 1 || fake.backups[0] != "appdb/s3" {
		t.Fatalf("backups run: %v", fake.backups)
	}
	if view := plain(d.render()); d.mode != modeTable || !strings.Contains(view, "appdb → s3: ok aabbccdd") {
		t.Errorf("outcome missing (mode %d):\n%s", d.mode, view)
	}
}

func TestBackupFailureIsShownNotHidden(t *testing.T) {
	fake := &fakeActions{failWith: errors.New("unknown job")}
	d := managerWith(t, fake)
	_, cmd := d.onKey("b", "")
	d = feed(t, d, messagesOf(cmd))
	if !d.noticeBad || !strings.Contains(plain(d.render()), "unknown job") {
		t.Errorf("want the failure on screen:\n%s", plain(d.render()))
	}
}

func TestBackupKeyOnlyActsOnTheBackupsTab(t *testing.T) {
	fake := &fakeActions{}
	d := managerWith(t, fake)
	model, _ := d.onKey("2", "")
	model, cmd := model.(manager).onKey("b", "")
	if cmd != nil || model.(manager).mode != modeTable || len(fake.backups) != 0 {
		t.Error("b on the Uptime tab must do nothing")
	}
}

func TestJobsThatNeverRanAreListed(t *testing.T) {
	d := managerWith(t, &fakeActions{})
	view := plain(d.render())
	if !strings.Contains(view, "newjob") || !strings.Contains(view, "never run") || !strings.Contains(view, "1 Backups (4)") {
		t.Errorf("never-run job missing:\n%s", view)
	}
}

func TestRestoreNeedsTargetAndTheJobNameThenShowsTheLoadCommand(t *testing.T) {
	fake := &fakeActions{}
	d := managerWith(t, fake)
	model, cmd := d.onKey("s", "")
	d = feed(t, model.(manager), messagesOf(cmd))
	if d.mode != modeSnapshots || !strings.Contains(plain(d.render()), "newest00") {
		t.Fatalf("snapshot list missing (mode %d):\n%s", d.mode, plain(d.render()))
	}
	model, _ = d.onKey("down", "")
	model, _ = model.(manager).onKey("enter", "")
	d = model.(manager)
	if d.mode != modeTarget || d.chosen.ID != "older00000000000" {
		t.Fatalf("mode %d chosen %q", d.mode, d.chosen.ID)
	}

	model, _ = d.onKey("enter", "") // empty target
	d = model.(manager)
	if d.mode != modeTarget || d.inputError == "" {
		t.Fatal("an empty or relative target must be refused")
	}
	d = typeText(t, d, "relative/dir")
	model, _ = d.onKey("enter", "")
	if model.(manager).mode != modeTarget {
		t.Fatal("a relative target must be refused")
	}
	d = model.(manager)
	for range "relative/dir" {
		model, _ = d.onKey("backspace", "")
		d = model.(manager)
	}
	d = typeText(t, d, "/tmp/restore")
	model, _ = d.onKey("enter", "")
	d = model.(manager)
	if d.mode != modeConfirm || !strings.Contains(plain(d.render()), "/tmp/restore") {
		t.Fatalf("confirm screen missing (mode %d):\n%s", d.mode, plain(d.render()))
	}

	d = typeText(t, d, "wrong")
	model, cmd = d.onKey("enter", "")
	d = model.(manager)
	if cmd != nil || len(fake.restored) != 0 || d.inputError == "" {
		t.Fatal("a wrong job name must not start the restore")
	}
	for range "wrong" {
		model, _ = d.onKey("backspace", "")
		d = model.(manager)
	}
	d = typeText(t, d, "appdb")
	model, cmd = d.onKey("enter", "")
	d = feed(t, model.(manager), messagesOf(cmd))
	if len(fake.restored) != 1 || fake.restored[0] != "appdb/s3/older00000000000" || fake.restoreTo != "/tmp/restore" {
		t.Fatalf("restore call: %v to %q", fake.restored, fake.restoreTo)
	}
	view := plain(d.render())
	if d.mode != modeResult || !strings.Contains(view, "Restored older000") || !strings.Contains(view, "pg_restore -d appdb /tmp/restore/appdb.dump") {
		t.Errorf("result screen (mode %d):\n%s", d.mode, view)
	}
	model, _ = d.onKey("x", "x")
	if model.(manager).mode != modeSnapshots {
		t.Error("a key after a successful restore returns to the snapshot list")
	}
}

func TestRestoreFailureIsShown(t *testing.T) {
	fake := &fakeActions{}
	d := managerWith(t, fake)
	model, cmd := d.onKey("s", "")
	d = feed(t, model.(manager), messagesOf(cmd))
	fake.failWith = errors.New("disk full")
	model, _ = d.onKey("enter", "")
	d = typeText(t, model.(manager), "/tmp/r")
	model, _ = d.onKey("enter", "")
	d = typeText(t, model.(manager), "appdb")
	model, cmd = d.onKey("enter", "")
	d = feed(t, model.(manager), messagesOf(cmd))
	if !d.resultBad || !strings.Contains(plain(d.render()), "Restore failed: disk full") {
		t.Errorf("failure missing:\n%s", plain(d.render()))
	}
}

func TestRestoreToRootWarns(t *testing.T) {
	d := managerWith(t, &fakeActions{})
	model, cmd := d.onKey("s", "")
	d = feed(t, model.(manager), messagesOf(cmd))
	model, _ = d.onKey("enter", "")
	d = typeText(t, model.(manager), "/")
	model, _ = d.onKey("enter", "")
	if view := plain(model.(manager).render()); !strings.Contains(view, "restored in place") {
		t.Errorf("want a warning for /:\n%s", view)
	}
}

func TestEscapeBacksOutOfRestore(t *testing.T) {
	d := managerWith(t, &fakeActions{})
	model, cmd := d.onKey("s", "")
	d = feed(t, model.(manager), messagesOf(cmd))
	model, _ = d.onKey("enter", "")
	model, _ = model.(manager).onKey("esc", "")
	if model.(manager).mode != modeSnapshots {
		t.Error("esc in the target prompt returns to the snapshot list")
	}
	model, _ = model.(manager).onKey("esc", "")
	if model.(manager).mode != modeTable {
		t.Error("esc in the snapshot list returns to the tabs")
	}
}

func TestCtrlCCancelsARunningActionButQDoesNotQuit(t *testing.T) {
	d := managerWith(t, &fakeActions{})
	ctx, cancel := context.WithCancel(context.Background())
	d.mode, d.cancelBusy = modeBusy, cancel
	model, cmd := d.onKey("q", "q")
	if cmd != nil || model.(manager).quitting {
		t.Error("q must not quit while an action runs")
	}
	model, _ = model.(manager).onKey("ctrl+c", "")
	if ctx.Err() == nil || !strings.Contains(model.(manager).busyText, "cancelling") {
		t.Error("ctrl+c must cancel the running action")
	}
}
