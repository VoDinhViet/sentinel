package monitor

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sentinel/internal/alert"
	"sentinel/internal/config"
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

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.msgs {
		out = append(out, m.Level.String()+" "+m.Title)
	}
	r.msgs = nil
	return out
}

func th(v float64, d time.Duration) *config.Threshold { return &config.Threshold{Value: v, For: d} }

func TestSustainedThreshold(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	m := &Monitor{Thresholds: config.Thresholds{CPU: th(90, 5*time.Minute)}, Alerts: &alert.Manager{Sender: rec}}
	t0 := time.Unix(1_700_000_000, 0)
	at := func(min int, cpu float64) Sample { return Sample{At: t0.Add(time.Duration(min) * time.Minute), CPU: cpu, Cores: 1} }

	// A short spike never alerts.
	m.Evaluate(ctx, at(0, 99))
	m.Evaluate(ctx, at(3, 99))
	m.Evaluate(ctx, at(4, 40))
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("spike alerted: %v", got)
	}

	// Sustained for 5 minutes alerts once.
	for min := 5; min <= 10; min++ {
		m.Evaluate(ctx, at(min, 95))
	}
	if got := rec.take(); len(got) != 1 || got[0] != "WARNING CPU usage high" {
		t.Fatalf("sustained load: %v", got)
	}

	m.Evaluate(ctx, at(11, 20))
	if got := rec.take(); len(got) != 1 || got[0] != "RESOLVED CPU usage back to normal" {
		t.Fatalf("recovery: %v", got)
	}
}

func TestInstantThresholdAndDisks(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	m := &Monitor{
		Thresholds: config.Thresholds{Disk: th(85, 0), LoadPerCore: th(2, 0)},
		Alerts:     &alert.Manager{Sender: rec},
	}
	now := time.Unix(1_700_000_000, 0)
	m.Evaluate(ctx, Sample{At: now, Cores: 4, Load1: 6, Disks: map[string]float64{"/": 50, "/data": 91}})
	got := rec.take()
	if len(got) != 1 || got[0] != "WARNING Disk usage on /data high" {
		t.Fatalf("got %v (load 6/4 cores = 1.5 is under 2)", got)
	}
	m.Evaluate(ctx, Sample{At: now, Cores: 4, Load1: 9, Disks: map[string]float64{"/": 50, "/data": 91}})
	if got := rec.take(); len(got) != 1 || got[0] != "WARNING Load per core (4 cores) high" {
		t.Fatalf("load: %v", got)
	}
}

type fakeSource struct{ s Sample }

func (f fakeSource) Sample(context.Context) (Sample, error) { return f.s, nil }

func TestTickRecordsMetrics(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	src := fakeSource{Sample{At: time.Unix(1_700_000_000, 0), CPU: 12.5, Memory: 40, Cores: 2, Disks: map[string]float64{"/": 30, "/data": 70}}}
	m := &Monitor{Source: src, Store: st, Alerts: &alert.Manager{}}
	m.Tick(ctx)
	got, ok, err := st.LatestMetrics(ctx)
	if err != nil || !ok || got.CPU != 12.5 || got.DiskMax != 70 {
		t.Fatalf("stored metrics = %+v, %v, %v", got, ok, err)
	}
}

func TestSystemSample(t *testing.T) {
	s, err := System{}.Sample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Cores < 1 || s.Memory <= 0 || s.Memory > 100 || len(s.Disks) == 0 {
		t.Errorf("implausible sample: %+v", s)
	}
}
