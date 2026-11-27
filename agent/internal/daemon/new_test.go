package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/store"
)

func TestNewSchedulesOneTaskPerUptimeCheck(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{
		ServerName: "h",
		UptimeChecks: []config.UptimeCheck{
			{Name: "web", Type: config.CheckHTTP, URL: "http://x", Interval: time.Minute},
			{Name: "db", Type: config.CheckTCP, Address: "127.0.0.1:1", Interval: time.Minute},
		},
	}
	d, err := New(context.Background(), cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	// One task per uptime check, then the disk watcher that is on unless disk.min_free_percent is 0.
	if len(d.Tasks) != 3 || d.Tasks[0].Name != "uptime:web" || d.Tasks[1].Name != "uptime:db" || d.Tasks[2].Name != "disk" {
		t.Errorf("tasks = %+v", d.Tasks)
	}
	if d.Runner.LockRetries != 3 || d.Alerts == nil {
		t.Errorf("runner/alerts not set up: %+v", d)
	}
}
