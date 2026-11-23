package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/internal/backup"
	"sentinel/internal/config"
)

func TestRunRefusesToStartWhileAnotherDaemonHoldsTheState(t *testing.T) {
	cfg := &config.Config{StorePath: filepath.Join(t.TempDir(), "state.db")}
	runner := &backup.Runner{Config: cfg}
	held, release, err := runner.LockDaemon() // the daemon that is already running
	if err != nil || !held {
		t.Fatalf("setup: held=%v err=%v", held, err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = (&Daemon{Config: cfg, Runner: runner}).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("Run = %v, want it to refuse because another daemon holds the state directory", err)
	}
}

func TestVerifyTimeoutFollowsTheSlowestJob(t *testing.T) {
	tests := []struct {
		name string
		jobs []config.Job
		want time.Duration
	}{
		{"no jobs", nil, 6 * time.Hour},
		{"jobs without a timeout", []config.Job{{Name: "a"}}, 6 * time.Hour},
		{"short job timeouts do not shorten it", []config.Job{{Name: "a", Timeout: 10 * time.Minute}}, 6 * time.Hour},
		{"the longest wins", []config.Job{{Name: "a", Timeout: time.Hour}, {Name: "b", Timeout: 12 * time.Hour}, {Name: "c", Timeout: 2 * time.Hour}}, 12 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Daemon{Config: &config.Config{Jobs: tt.jobs}}
			if got := d.verifyTimeout(); got != tt.want {
				t.Errorf("verifyTimeout = %v, want %v", got, tt.want)
			}
		})
	}
}
