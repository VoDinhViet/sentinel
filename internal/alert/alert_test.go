package alert

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel/internal/notify"
	"sentinel/internal/store"
)

type recorder struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (r *recorder) Send(_ context.Context, m notify.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *recorder) take() []notify.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.msgs
	r.msgs = nil
	return out
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newManager(t *testing.T, st *store.Store) (*Manager, *recorder, *clock) {
	rec := &recorder{}
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	m := &Manager{Sender: rec, Store: st, Server: "web-01", Cooldown: time.Hour, Now: clk.now}
	if err := m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m, rec, clk
}

func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	m, rec, clk := newManager(t, nil)

	// Healthy checks never notify.
	m.Resolve(ctx, "uptime:web", "web is up", "")
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("resolve on healthy key sent %v", got)
	}

	m.Fire(ctx, "uptime:web", notify.Critical, "web is down", "HTTP 502")
	got := rec.take()
	if len(got) != 1 || got[0].Level != notify.Critical || got[0].Server != "web-01" || got[0].Body != "HTTP 502" {
		t.Fatalf("first fire = %+v", got)
	}
	if !m.Firing("uptime:web") {
		t.Error("Firing() = false after Fire")
	}

	// Repeated failures within the cooldown are silent.
	clk.advance(30 * time.Minute)
	m.Fire(ctx, "uptime:web", notify.Critical, "web is down", "HTTP 502")
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("fire within cooldown sent %v", got)
	}

	// After the cooldown, one reminder.
	clk.advance(31 * time.Minute)
	m.Fire(ctx, "uptime:web", notify.Critical, "web is down", "HTTP 502")
	got = rec.take()
	if len(got) != 1 || !strings.Contains(got[0].Body, "Still failing") {
		t.Fatalf("reminder = %+v", got)
	}

	clk.advance(10 * time.Minute)
	m.Resolve(ctx, "uptime:web", "web is up", "")
	got = rec.take()
	if len(got) != 1 || got[0].Level != notify.Resolved || !strings.Contains(got[0].Body, "Recovered after 1h11m") {
		t.Fatalf("resolve = %+v", got)
	}
	m.Resolve(ctx, "uptime:web", "web is up", "")
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("second resolve sent %v", got)
	}
}

func TestKeysAreIndependent(t *testing.T) {
	ctx := context.Background()
	m, rec, _ := newManager(t, nil)
	m.Fire(ctx, "a", notify.Warning, "a broke", "")
	m.Fire(ctx, "b", notify.Warning, "b broke", "")
	m.Resolve(ctx, "a", "a ok", "")
	if got := rec.take(); len(got) != 3 {
		t.Fatalf("want 3 messages, got %+v", got)
	}
	if m.Firing("a") || !m.Firing("b") {
		t.Error("state mixed between keys")
	}
}

func TestStatePersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m1, rec1, _ := newManager(t, st)
	m1.Fire(ctx, "disk:/", notify.Warning, "disk full", "")
	if len(rec1.take()) != 1 {
		t.Fatal("first fire should notify")
	}

	// A new manager (daemon restart) must not re-announce the open alert...
	m2, rec2, _ := newManager(t, st)
	m2.Fire(ctx, "disk:/", notify.Warning, "disk full", "")
	if got := rec2.take(); len(got) != 0 {
		t.Fatalf("restart re-announced alert: %+v", got)
	}
	// ...but must still announce its resolution.
	m2.Resolve(ctx, "disk:/", "disk ok", "")
	if got := rec2.take(); len(got) != 1 || got[0].Level != notify.Resolved {
		t.Fatalf("resolve after restart = %+v", got)
	}
}
