package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"sentinel/internal/api"
	"sentinel/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newService(t *testing.T) (*Service, *clock) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	s.Now = clk.now
	return s, clk
}

func TestAgentLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)

	a, token, err := s.CreateAgent(ctx, "web-01")
	if err != nil || a.ID == 0 || len(token) < 40 {
		t.Fatalf("create = %+v, %q, %v", a, token, err)
	}
	if _, _, err := s.CreateAgent(ctx, "web-01"); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name err = %v", err)
	}
	if _, _, err := s.CreateAgent(ctx, "bad name!"); err == nil {
		t.Error("invalid name accepted")
	}

	got, err := s.Authenticate(ctx, token)
	if err != nil || got.ID != a.ID {
		t.Fatalf("authenticate = %+v, %v", got, err)
	}
	if _, err := s.Authenticate(ctx, "snt_wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong token err = %v", err)
	}

	newTok, err := s.RotateToken(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Error("old token still valid after rotation")
	}
	if _, err := s.Authenticate(ctx, newTok); err != nil {
		t.Errorf("new token rejected: %v", err)
	}

	if err := s.DeleteAgent(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAgent(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted agent err = %v", err)
	}
}

func TestIngestAndStatus(t *testing.T) {
	ctx := context.Background()
	s, clk := newService(t)
	a, _, _ := s.CreateAgent(ctx, "web-01")
	at := clk.t.Add(-time.Hour)

	report := api.Report{
		Hostname: "web-01.local", Version: "1.0",
		Jobs: []api.Job{{Name: "db", Type: "postgres", Repositories: []string{"s3"}}},
		BackupRuns: []store.BackupRun{
			{ID: 1, Job: "db", Repository: "s3", StartedAt: at, FinishedAt: at, Success: true, SnapshotID: "aaa"},
			{ID: 2, Job: "db", Repository: "s3", StartedAt: at, FinishedAt: at.Add(time.Minute), Success: false, Error: "boom"},
		},
		Results:    []store.CheckResult{{ID: 7, Name: "web", At: at, OK: true, Latency: 30 * time.Millisecond}},
		Metrics:    []store.Metrics{{ID: 3, At: at, CPU: 12}, {ID: 4, At: at.Add(time.Minute), CPU: 34}},
		OpenAlerts: []store.Alert{{Key: "backup:db:s3", Since: at, Message: "Backup db to s3 failed", Firing: true}},
	}
	cur, err := s.Ingest(ctx, a.ID, report)
	if err != nil {
		t.Fatal(err)
	}
	if cur != (api.Cursor{BackupRunID: 2, CheckID: 7, MetricID: 4}) {
		t.Errorf("cursor = %+v", cur)
	}
	// Re-sending the same batch is harmless.
	if _, err := s.Ingest(ctx, a.ID, report); err != nil {
		t.Fatalf("resend: %v", err)
	}

	st, err := s.Status(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Agent.Hostname != "web-01.local" || len(st.Agent.Jobs) != 1 || !st.Agent.LastSeen.Equal(clk.t) {
		t.Errorf("agent = %+v", st.Agent)
	}
	if len(st.Backups) != 1 || st.Backups[0].Success || st.Backups[0].Error != "boom" {
		t.Errorf("latest backups = %+v", st.Backups)
	}
	if len(st.Alerts) != 1 || len(st.Checks) != 1 || st.Metrics == nil || st.Metrics.CPU != 34 {
		t.Errorf("status = %+v", st)
	}
	if !st.Agent.Online(clk.t.Add(time.Minute), 3*time.Minute) || st.Agent.Online(clk.t.Add(5*time.Minute), 3*time.Minute) {
		t.Error("Online() wrong")
	}

	// Alerts are a snapshot: an empty list clears them.
	report.OpenAlerts, report.BackupRuns, report.Results, report.Metrics = nil, nil, nil, nil
	s.Ingest(ctx, a.ID, report)
	if st, _ := s.Status(ctx, a.ID); len(st.Alerts) != 0 {
		t.Errorf("alerts not cleared: %+v", st.Alerts)
	}

	series, err := s.MetricSeries(ctx, a.ID, at)
	if err != nil || len(series) != 2 || series[0].CPU != 12 {
		t.Errorf("series = %+v, %v", series, err)
	}

	big := api.Report{Metrics: make([]store.Metrics, api.MaxBatch+1)}
	if _, err := s.Ingest(ctx, a.ID, big); err == nil {
		t.Error("oversized batch accepted")
	}
}
