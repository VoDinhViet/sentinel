package status

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/store"
	"sentinel/pkg/alert"
)

// The report is printed by `sentinel status` and drawn by `sentinel manage`. Its texts come from the store,
// which holds restic and uptime errors that quote file names and remote answers, including rows written by
// older versions, so the report cleans them instead of trusting what was stored.
func TestBuildStripsEscapeSequencesFromStoredText(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const hostile = "read /srv/\x1b]52;c;ZXZpbA==\x07evil\x1b[2K failed"
	now := time.Now()
	if err := st.RecordBackup(ctx, store.BackupRun{Job: "files", Repository: "s3", StartedAt: now, FinishedAt: now, Error: hostile}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordBackup(ctx, store.BackupRun{Job: "db", Repository: "s3", StartedAt: now, FinishedAt: now, Success: true, Error: hostile}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordCheck(ctx, store.CheckResult{Name: "site", At: now, Error: hostile}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAlert(ctx, alert.Alert{Key: "backup:files:s3", Firing: true, Since: now, Message: hostile}); err != nil {
		t.Fatal(err)
	}

	report, err := Build(ctx, &config.Config{ServerName: "web-01"}, st, now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	// JSON escapes control characters itself, so look at the decoded strings, which is what the screens print.
	var decoded Report
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, alert := range decoded.Alerts {
		texts = append(texts, alert.Message)
	}
	for _, job := range decoded.Jobs {
		texts = append(texts, job.Result, job.Warning, job.Error)
	}
	for _, check := range decoded.Checks {
		texts = append(texts, check.Error)
	}
	var withText int
	for _, text := range texts {
		if strings.ContainsAny(text, "\x1b\x07") {
			t.Errorf("text still has a control character: %q", text)
		}
		if strings.Contains(text, "/srv/") {
			withText++
		}
	}
	if withText < 4 {
		t.Errorf("only %d of the texts kept their content, want the alert message, a result, a warning/error and a check error: %q", withText, texts)
	}
}
