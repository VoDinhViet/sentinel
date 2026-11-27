package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/notify"
	"sentinel/server/internal/config"
)

type mailbox struct {
	mu       sync.Mutex
	messages []notify.Message
}

func (m *mailbox) Name() string { return "test" }
func (m *mailbox) Send(_ context.Context, msg notify.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

func TestTheDigestSaysHowTheServersAndTheBackupsStand(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	if _, err := Init(ctx, cfg, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	a, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	now := time.Now()
	for _, name := range []string{"web01", "db01", "old01"} {
		if _, err := a.Store.DB.Exec(`INSERT INTO agents (name, state, created_at, enrolled_at, last_seen) VALUES (?, ?, 1, ?, ?)`, name, map[bool]string{true: "revoked", false: "active"}[name == "old01"], now.Unix(), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Store.DB.Exec(`UPDATE agents SET last_seen = ? WHERE name = 'db01'`, now.Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := a.Report.Record(ctx, "web01", agentapi.Report{Backups: []agentapi.BackupStatus{{Job: "db", Repository: "s3", FinishedAt: now.Add(-time.Hour), Result: "FAILED: disk full"}}}, now); err != nil {
		t.Fatal(err)
	}
	box := &mailbox{}
	if err := a.SendDigest(ctx, &notify.Multi{Notifiers: []notify.Notifier{box}}, now); err != nil {
		t.Fatal(err)
	}
	if len(box.messages) != 1 {
		t.Fatalf("%d messages", len(box.messages))
	}
	message := box.messages[0]
	for _, want := range []string{"Servers: 2, 1 OFFLINE, 1 ONLINE", "db01 OFFLINE", "1 runs, 1 failed", "web01 db/s3"} {
		if !strings.Contains(message.Body, want) {
			t.Errorf("lacks %q:\n%s", want, message.Body)
		}
	}
	if message.Level != notify.Warning || message.Server != "sentinel-server" {
		t.Errorf("%+v", message)
	}
}

// serve ends when any of its parts returns, so a digest that is off must stay until it is told to stop.
func TestTheDigestThatIsOffWaitsForTheEndOfTheServer(t *testing.T) {
	a := &App{Config: &config.Config{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.RunDigest(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("RunDigest returned at once: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("%v", err)
	}
}
