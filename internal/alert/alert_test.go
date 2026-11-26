package alert

import (
	"context"
	"errors"
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

func (r *recorder) Deliver(_ context.Context, m notify.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *recorder) take() []notify.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs := r.msgs
	r.msgs = nil
	return msgs
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newManager(t *testing.T, st *store.Store) (*Manager, *recorder, *clock) {
	rec := &recorder{}
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	m := &Manager{Notifier: rec, Store: st, Server: "web-01", Cooldown: time.Hour, Now: clk.now}
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

// outage is a notifier that takes nothing while down is set.
type outage struct {
	recorder
	down bool
}

func (o *outage) Deliver(ctx context.Context, msg notify.Message) error {
	if o.down {
		return errors.New("every notifier failed")
	}
	return o.recorder.Deliver(ctx, msg)
}

// A message that no notifier takes is not lost: it goes out when they are back, once.
func TestUndeliveredMessagesAreSentAgain(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	notifiers := &outage{down: true}
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	m := &Manager{Notifier: notifiers, Store: st, Server: "web-01", Cooldown: time.Hour, Now: clk.now}
	if err := m.Load(ctx); err != nil {
		t.Fatal(err)
	}

	m.Fire(ctx, "uptime:web", notify.Critical, "web is down", "HTTP 502")
	if got := notifiers.take(); len(got) != 0 {
		t.Fatalf("delivered during the outage: %v", got)
	}
	m.RetryPending(ctx)
	if got := notifiers.take(); len(got) != 0 {
		t.Fatalf("delivered during the outage: %v", got)
	}

	notifiers.down = false
	m.RetryPending(ctx)
	got := notifiers.take()
	if len(got) != 1 || got[0].Level != notify.Critical || got[0].Title != "web is down" || got[0].Body != "HTTP 502" || got[0].Server != "web-01" {
		t.Fatalf("retry = %+v, want the opening message once", got)
	}
	m.RetryPending(ctx)
	if got := notifiers.take(); len(got) != 0 {
		t.Fatalf("a delivered message was sent again: %v", got)
	}
}

// The resolution of an alert is as worth keeping as its opening.
func TestUndeliveredResolutionIsSentAgain(t *testing.T) {
	ctx := context.Background()
	notifiers := &outage{}
	m := &Manager{Notifier: notifiers, Server: "web-01", Cooldown: time.Hour}
	m.Fire(ctx, "uptime:web", notify.Critical, "web is down", "")
	notifiers.take()

	notifiers.down = true
	m.Resolve(ctx, "uptime:web", "web is up", "")
	notifiers.down = false
	m.RetryPending(ctx)
	if got := notifiers.take(); len(got) != 1 || got[0].Level != notify.Resolved {
		t.Fatalf("retry = %+v, want the resolution", got)
	}
}

// A restart in the middle of an outage keeps the waiting message, and a newer message of the same alert replaces it.
func TestPendingMessageSurvivesRestartAndNewerOneReplacesIt(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	down := &outage{down: true}
	first := &Manager{Notifier: down, Store: st, Server: "web-01", Cooldown: time.Hour}
	if err := first.Load(ctx); err != nil {
		t.Fatal(err)
	}
	first.Fire(ctx, "disk:/", notify.Warning, "disk full", "95% used")

	up := &outage{}
	second := &Manager{Notifier: up, Store: st, Server: "web-01", Cooldown: time.Hour}
	if err := second.Load(ctx); err != nil {
		t.Fatal(err)
	}
	// Fixed before the retry ran: only the resolution is worth sending.
	down.down = true
	second.Notifier = down
	second.Resolve(ctx, "disk:/", "disk ok", "")
	second.Notifier = up
	second.RetryPending(ctx)
	got := up.take()
	if len(got) != 1 || got[0].Level != notify.Resolved {
		t.Fatalf("after restart = %+v, want only the resolution", got)
	}
}

// Without notifiers there is nobody to send to, so nothing waits.
func TestNoNotifierLeavesNothingPending(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := &Manager{Store: st, Cooldown: time.Hour}
	m.Fire(ctx, "uptime:web", notify.Critical, "web is down", "")
	stored, err := st.Alerts(ctx, false)
	if err != nil || len(stored) != 1 || stored[0].Pending != nil {
		t.Fatalf("stored = %+v, %v; want one alert with nothing pending", stored, err)
	}
}
