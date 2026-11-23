package status

import (
	"context"
	"encoding/json"
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

func TestReportJSONKeys(t *testing.T) {
	encoded, err := json.Marshal(Report{Jobs: []Job{{Job: "db"}}})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &keys); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server", "generated_at", "alerts", "jobs", "checks"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("JSON lacks the %q key: %s", want, encoded)
		}
	}
	if _, old := keys["backups"]; old {
		t.Errorf("the old %q key is still there: %s", "backups", encoded)
	}
}

func TestReportCarriesTheRecentResultsOfEachCheck(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Now().Add(-time.Hour)
	for _, result := range []struct {
		ok      bool
		latency time.Duration
	}{{true, 10 * time.Millisecond}, {false, 0}, {true, 30 * time.Millisecond}} {
		at = at.Add(time.Minute)
		if err := st.RecordCheck(ctx, store.CheckResult{Name: "web", At: at, OK: result.ok, Latency: result.latency}); err != nil {
			t.Fatal(err)
		}
	}

	report, err := Build(ctx, &config.Config{ServerName: "web-01"}, st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Checks) != 1 {
		t.Fatalf("checks = %+v", report.Checks)
	}
	check := report.Checks[0]
	if !slices.Equal(check.RecentLatencyMs, []int64{10, 0, 30}) || !slices.Equal(check.RecentOK, []bool{true, false, true}) {
		t.Errorf("recent latency %v, ok %v: oldest first", check.RecentLatencyMs, check.RecentOK)
	}
}
