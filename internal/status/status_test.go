package status

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/store"
)

func TestReportCarriesTheRecentOutcomesOfEachJob(t *testing.T) {
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

	report, err := Build(ctx, &config.Config{ServerName: "web-01"}, st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, job := range report.Jobs {
		got[job.Job] = job.Recent
	}
	if !slices.Equal(got["appdb"], []string{"ok", "warning", "failed"}) || !slices.Equal(got["files"], []string{"ok"}) {
		t.Errorf("recent = %v", got)
	}
}
