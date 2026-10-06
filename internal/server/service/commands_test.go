package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"sentinel/internal/api"
)

func agentWithJobs(t *testing.T, s *Service) Agent {
	t.Helper()
	ctx := context.Background()
	a, _, err := s.CreateAgent(ctx, "web-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, a.ID, api.Report{Jobs: []api.Job{{Name: "db", Repositories: []string{"s3", "gdrive"}}}}); err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAgent(ctx, a.ID)
	return a
}

func TestCommandValidation(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	a := agentWithJobs(t, s)
	bad := []any{
		api.BackupRunArgs{Job: "nope"},
		api.SnapshotsArgs{Job: "db", Repository: "local"},
		api.RestoreArgs{Job: "db", Repository: "s3", Snapshot: "../../etc"},
		"not a command",
	}
	for _, args := range bad {
		if _, err := s.Enqueue(ctx, a.ID, args); err == nil {
			t.Errorf("Enqueue(%+v) accepted", args)
		}
	}
	if _, err := s.Enqueue(ctx, a.ID, api.RestoreArgs{Job: "db", Repository: "s3", Snapshot: "abcdef12"}); err != nil {
		t.Errorf("valid restore rejected: %v", err)
	}
}

func TestCommandLifecycle(t *testing.T) {
	ctx := context.Background()
	s, clk := newService(t)
	a := agentWithJobs(t, s)

	// Long-poll wakes up when a command is queued.
	got := make(chan []api.Command, 1)
	go func() {
		cmds, _ := s.WaitCommands(ctx, a.ID, 5*time.Second)
		got <- cmds
	}()
	time.Sleep(50 * time.Millisecond)
	id, err := s.Enqueue(ctx, a.ID, api.BackupRunArgs{Job: "db"})
	if err != nil {
		t.Fatal(err)
	}
	var cmds []api.Command
	select {
	case cmds = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("long-poll did not wake")
	}
	if len(cmds) != 1 || cmds[0].ID != id || cmds[0].Type != api.CmdBackupRun {
		t.Fatalf("claimed = %+v", cmds)
	}
	rec, _ := s.GetCommand(ctx, a.ID, id)
	if rec.Status != api.StatusRunning || rec.Describe() != "Backup db" {
		t.Errorf("record = %+v", rec)
	}

	// Nothing left to claim; an empty wait returns after the timeout.
	if cmds, _ := s.WaitCommands(ctx, a.ID, 10*time.Millisecond); len(cmds) != 0 {
		t.Errorf("claimed twice: %+v", cmds)
	}

	// Another agent cannot complete it.
	other, _, _ := s.CreateAgent(ctx, "web-02")
	if err := s.Complete(ctx, other.ID, id, api.CommandResult{OK: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign complete err = %v", err)
	}
	out, _ := json.Marshal(api.RestoreOutput{Path: "/x"})
	if err := s.Complete(ctx, a.ID, id, api.CommandResult{OK: true, Output: out}); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, a.ID, id, api.CommandResult{OK: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("double complete err = %v", err)
	}
	rec, _ = s.GetCommand(ctx, a.ID, id)
	if rec.Status != api.StatusDone || !rec.Done() || string(rec.Output) != string(out) {
		t.Errorf("finished record = %+v", rec)
	}

	// Expiry: a pending command for an offline agent times out.
	stale, _ := s.Enqueue(ctx, a.ID, api.BackupRunArgs{Job: "db"})
	clk.advance(25 * time.Hour)
	if err := s.ExpireCommands(ctx, 7*time.Hour, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.GetCommand(ctx, a.ID, stale); rec.Status != api.StatusTimeout {
		t.Errorf("stale status = %s", rec.Status)
	}
	list, _ := s.ListCommands(ctx, a.ID, 10)
	if len(list) != 2 || list[0].ID != stale {
		t.Errorf("list = %+v", list)
	}
}
