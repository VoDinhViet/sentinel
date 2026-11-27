package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/restic"
)

func serviceWithJob(t *testing.T) *Service {
	t.Helper()
	cfg := &config.Config{
		StorePath: filepath.Join(t.TempDir(), "state.db"),
		Jobs:      []config.Job{{Name: "shop", Repositories: []string{"local"}}},
	}
	return New(cfg, nil, filepath.Join(t.TempDir(), "config.yaml"))
}

func TestStartBackupRejectsWhatTheConfigDoesNotHave(t *testing.T) {
	svc := serviceWithJob(t)
	tests := map[string]struct {
		jobName, repo, want string
	}{
		"unknown job":         {"nope", "", "not found"},
		"repository not used": {"shop", "s3", "not found"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := svc.StartBackup(tc.jobName, tc.repo); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestSnapshotsAndRestoreRejectAnUnknownJob(t *testing.T) {
	svc := serviceWithJob(t)
	if _, err := svc.ListSnapshots(context.Background(), "nope", "local"); err == nil {
		t.Error("ListSnapshots of an unknown job must fail")
	}
	if _, err := svc.Restore(context.Background(), "nope", "local", restic.Snapshot{ID: "abc"}, t.TempDir()); err == nil {
		t.Error("Restore of an unknown job must fail")
	}
}
