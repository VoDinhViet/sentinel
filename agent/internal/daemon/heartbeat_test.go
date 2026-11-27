package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"sentinel/agent/internal/alert"
	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/store"
)

func TestHeartbeatTaskIsAbsentWithoutAURL(t *testing.T) {
	if tasks := heartbeatTask(&config.Config{}); len(tasks) != 0 {
		t.Errorf("tasks = %+v, want none", tasks)
	}
}

// The daemon says it is alive as soon as it starts, not only after the first interval.
func TestHeartbeatIsSentAtStartAndOnTheInterval(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	cfg := &config.Config{ServerName: "h", Heartbeat: config.Heartbeat{URL: server.URL, Every: time.Hour}}
	tasks := heartbeatTask(cfg)
	if len(tasks) != 1 || !tasks[0].AtStart || tasks[0].Every != time.Hour {
		t.Fatalf("tasks = %+v", tasks)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := &Daemon{Config: cfg, Store: st, Alerts: &alert.Manager{}, Runner: &backup.Runner{Config: cfg}, Tasks: tasks, ShutdownTimeout: 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for requests.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("%d heartbeat requests at start, want 1", got)
	}
}
