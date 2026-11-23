package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/store"
)

func TestStatusReportCarriesTheRecentOutcomesOfEachJob(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Now().Add(-time.Hour)
	record := func(job string, success bool, warning string) {
		t.Helper()
		at = at.Add(time.Minute)
		run := store.BackupRun{Job: job, Repository: "s3", StartedAt: at, FinishedAt: at, Success: success, Error: warning}
		if !success && warning == "" {
			run.Error = "boom"
		}
		if err := st.RecordBackup(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	record("appdb", true, "")
	record("appdb", true, "retention failed: x")
	record("appdb", false, "")
	record("files", true, "")

	report, err := buildStatusReport(ctx, &config.Config{ServerName: "web-01"}, st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, backup := range report.Backups {
		got[backup.Job] = backup.Recent
	}
	if !slices.Equal(got["appdb"], []string{"ok", "warning", "failed"}) || !slices.Equal(got["files"], []string{"ok"}) {
		t.Errorf("recent = %v", got)
	}
}

func TestRelativeAgo(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{59 * time.Minute, "59m ago"},
		{3 * time.Hour, "3h ago"},
		{47 * time.Hour, "47h ago"},
		{72 * time.Hour, "3d ago"},
	} {
		if got := relativeAgo(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("relativeAgo(%v ago) = %q, want %q", tc.ago, got, tc.want)
		}
	}
	if got := relativeAgo(time.Time{}, now); got != "never" {
		t.Errorf("zero time = %q, want never", got)
	}
}
