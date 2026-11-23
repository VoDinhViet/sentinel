package service

import (
	"context"
	"errors"
	"testing"

	"sentinel/internal/api"
)

func TestUserLifecycleAndSessions(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)

	for _, bad := range []struct{ name, pass, role string }{
		{"ab", "long enough pw", RoleViewer}, // username too short
		{"bad name", "long enough pw", RoleViewer},
		{"alice", "short", RoleViewer}, // password too short
		{"alice", "long enough pw", "superuser"},
	} {
		if _, err := s.CreateUser(ctx, bad.name, bad.pass, bad.role); err == nil {
			t.Errorf("CreateUser(%+v) accepted", bad)
		}
	}
	alice, err := s.CreateUser(ctx, "alice", "long enough pw", RoleOperator)
	if err != nil || alice.ID == 0 {
		t.Fatalf("create = %+v, %v", alice, err)
	}
	if _, err := s.CreateUser(ctx, "ALICE", "long enough pw", RoleViewer); !errors.Is(err, ErrConflict) {
		t.Errorf("usernames must be unique ignoring case, got %v", err)
	}

	if _, err := s.VerifyUser(ctx, "alice", "wrong password"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong password = %v", err)
	}
	if _, err := s.VerifyUser(ctx, "nobody", "long enough pw"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("unknown user = %v", err)
	}
	actor, err := s.VerifyUser(ctx, "Alice", "long enough pw")
	if err != nil || actor.UserID != alice.ID || actor.Role != RoleOperator || actor.Username != "alice" {
		t.Fatalf("verify = %+v, %v", actor, err)
	}

	token, _, err := s.CreateSession(ctx, actor, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if _, got, err := s.Session(ctx, token); err != nil || got.Role != RoleOperator {
		t.Fatalf("session = %+v, %v", got, err)
	}

	// A role change applies to the live session.
	viewer := RoleViewer
	if _, err := s.UpdateUser(ctx, alice.ID, UserUpdate{Role: &viewer}); err != nil {
		t.Fatal(err)
	}
	if _, got, _ := s.Session(ctx, token); got.Role != RoleViewer {
		t.Errorf("role after update = %q", got.Role)
	}

	// Disabling signs the user out and blocks login.
	yes := true
	if _, err := s.UpdateUser(ctx, alice.ID, UserUpdate{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Session(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("session of disabled user = %v", err)
	}
	if _, err := s.VerifyUser(ctx, "alice", "long enough pw"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("disabled user can log in: %v", err)
	}

	// A password change invalidates sessions and the old password.
	no, newPass := false, "another long password"
	s.UpdateUser(ctx, alice.ID, UserUpdate{Disabled: &no})
	token2, _, _ := s.CreateSession(ctx, actor, 1<<40)
	if _, err := s.UpdateUser(ctx, alice.ID, UserUpdate{Password: &newPass}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Session(ctx, token2); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("session survived password change: %v", err)
	}
	if _, err := s.VerifyUser(ctx, "alice", "long enough pw"); err == nil {
		t.Error("old password still works")
	}
	if _, err := s.VerifyUser(ctx, "alice", newPass); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
	if _, err := s.UpdateUser(ctx, 999, UserUpdate{Role: &viewer}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of unknown user = %v", err)
	}
}

func TestBootstrapAdminSessionIsAdmin(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	token, _, _ := s.CreateSession(ctx, Actor{Username: "admin"}, 1<<40)
	if _, got, err := s.Session(ctx, token); err != nil || got.Role != RoleAdmin || got.Username != "admin" || got.UserID != 0 {
		t.Fatalf("session = %+v, %v", got, err)
	}
}

func TestRoleAtLeast(t *testing.T) {
	for _, tc := range []struct {
		have, need string
		want       bool
	}{
		{RoleAdmin, RoleViewer, true}, {RoleAdmin, RoleAdmin, true}, {RoleOperator, RoleViewer, true},
		{RoleOperator, RoleAdmin, false}, {RoleViewer, RoleOperator, false},
		{"", RoleViewer, false}, {"root", RoleViewer, false},
	} {
		if got := RoleAtLeast(tc.have, tc.need); got != tc.want {
			t.Errorf("RoleAtLeast(%q, %q) = %v", tc.have, tc.need, got)
		}
	}
}

func TestAuditIsAppendOnlyAndFilterable(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{UserID: 7, Username: "alice", Role: RoleOperator})
	s, _ := newService(t)

	for _, e := range []AuditEntry{
		{Action: "backup.run", AgentID: 1, AgentName: "web-01", Job: "db", Details: map[string]any{"x": 1}},
		{Action: "config.save", AgentID: 1, AgentName: "web-01"},
		{Action: "config.rollback", AgentID: 2, AgentName: "web-02", Outcome: OutcomeDenied},
		{Username: "mallory", Action: "auth.login", Outcome: OutcomeFailed},
	} {
		if err := s.Audit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.ListAudit(ctx, AuditFilter{})
	if len(all) != 4 || all[0].Action != "auth.login" || all[3].Username != "alice" || all[3].UserID != 7 || all[3].Details["x"] != float64(1) {
		t.Fatalf("entries = %+v", all)
	}
	if got, _ := s.ListAudit(ctx, AuditFilter{Action: "config."}); len(got) != 2 {
		t.Errorf("prefix filter = %d entries", len(got))
	}
	if got, _ := s.ListAudit(ctx, AuditFilter{Username: "MALLORY"}); len(got) != 1 {
		t.Errorf("user filter = %d entries", len(got))
	}
	if got, _ := s.ListAudit(ctx, AuditFilter{AgentID: 2, Outcome: OutcomeDenied}); len(got) != 1 {
		t.Errorf("agent+outcome filter = %d entries", len(got))
	}
	if got, _ := s.ListAudit(ctx, AuditFilter{BeforeID: all[1].ID, Limit: 1}); len(got) != 1 || got[0].ID != all[2].ID {
		t.Errorf("paging = %+v", got)
	}

	if _, err := s.db.Exec(`UPDATE audit_log SET username = 'x'`); err == nil {
		t.Error("audit entries can be modified")
	}
	if _, err := s.db.Exec(`DELETE FROM audit_log`); err == nil {
		t.Error("audit entries can be deleted")
	}
}

func TestActorIsRecordedOnCommandsAndConfigs(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{UserID: 3, Username: "bob", Role: RoleOperator})
	s, _ := newService(t)
	a := agentWithJobs(t, s)

	id, err := s.Enqueue(ctx, a.ID, api.BackupRunArgs{Job: "db"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd, _ := s.GetCommand(ctx, a.ID, id); cmd.RequestedBy != "bob" {
		t.Errorf("requested_by = %q", cmd.RequestedBy)
	}

	if _, err := s.SaveConfig(ctx, a.ID, goodConfig, "first"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.ConfigVersion(ctx, a.ID, 1); v.Author != "bob" {
		t.Errorf("author = %q", v.Author)
	}
	other := WithActor(context.Background(), Actor{Username: "agent"})
	if imported, _ := s.ImportConfig(other, a.ID, goodConfig); imported {
		t.Fatal("config already exists")
	}
}
