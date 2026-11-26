package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/internal/alert"
	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/store"
)

// The history is pruned when the daemon starts, not only 24 hours later: a daemon that is restarted
// more often than that (every change of the config asks for it) would otherwise never prune.
func TestPruneHistoryRunsAtStart(t *testing.T) {
	for _, task := range (&Daemon{Alerts: nil}).builtinTasks() {
		if task.Name == "prune-history" {
			if !task.AtStart {
				t.Error("prune-history does not run at start")
			}
			return
		}
	}
	t.Error("no prune-history task")
}

func TestPruneRepositoriesPrunesEachUsedRepositoryOnceAndAlertsOnFailure(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	argsFile, failFlag := filepath.Join(dir, "args"), filepath.Join(dir, "fail")
	bin := filepath.Join(dir, "restic")
	script := "#!/bin/sh\necho \"$*\" >> " + argsFile + "\n" +
		"if [ -f " + failFlag + " ]; then echo 'Fatal: unable to create lock' >&2; exit 1; fi\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{
		ServerName: "h",
		Repositories: map[string]config.Repository{
			"s3": {URL: "/r1", Password: "p"}, "local": {URL: "/r2", Password: "p"}, "unused": {URL: "/r3", Password: "p"},
			"keeps-all": {URL: "/r4", Password: "p"},
		},
		Jobs: []config.Job{
			{Name: "a", Type: config.JobFiles, Paths: []string{"/x"}, Timeout: time.Minute, Repositories: []string{"s3", "local"}, Retention: config.Retention{Last: 3}},
			{Name: "b", Type: config.JobFiles, Paths: []string{"/y"}, Timeout: time.Minute, Repositories: []string{"s3"}},
			{Name: "c", Type: config.JobFiles, Paths: []string{"/z"}, Timeout: time.Minute, Repositories: []string{"keeps-all"}}, // no retention: nothing to prune
		},
	}
	rec := &recorder{}
	d := &Daemon{
		Config: cfg, Store: st,
		Alerts: &alert.Manager{Notifier: rec, Store: st, Server: "h", Cooldown: time.Hour},
		Runner: &backup.Runner{Config: cfg, Store: st, Bin: bin},
	}

	d.PruneRepositories(ctx)
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	if len(commands) != 2 {
		t.Fatalf("one prune for each of the two repositories the jobs use, none for the unused one nor the one whose jobs forget nothing: %q", commands)
	}
	for _, command := range commands {
		if !strings.Contains(command, "prune") || strings.Contains(command, "/r3") || strings.Contains(command, "/r4") {
			t.Errorf("command %q", command)
		}
	}
	if got := rec.titles(); len(got) != 0 {
		t.Errorf("a good prune says nothing, got %v", got)
	}

	if err := os.WriteFile(failFlag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	d.PruneRepositories(ctx)
	got := rec.titles()
	if len(got) != 2 || !strings.HasPrefix(got[0], "WARNING Repository prune failed: ") {
		t.Fatalf("a failed prune must reach the alerts, got %v", got)
	}
	if err := os.Remove(failFlag); err != nil {
		t.Fatal(err)
	}
	d.PruneRepositories(ctx)
	if got := rec.titles(); len(got) != 2 || !strings.HasPrefix(got[0], "RESOLVED Repository prune succeeded: ") {
		t.Errorf("a prune that works again closes the alert, got %v", got)
	}
}
