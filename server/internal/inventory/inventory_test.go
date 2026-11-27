package inventory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.DB.Exec(`INSERT INTO agents (name, state, created_at, enrolled_at) VALUES ('web01', 'active', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	return New(st), st
}

func TestStatusOf(t *testing.T) {
	now := time.Now()
	after := 2 * time.Minute
	for name, tc := range map[string]struct {
		state          string
		enrolled, seen time.Time
		want           Status
	}{
		"pending":             {"pending", time.Time{}, time.Time{}, Pending},
		"revoked":             {"revoked", now, now, Revoked},
		"heard a moment ago":  {"active", now.Add(-time.Hour), now.Add(-10 * time.Second), Online},
		"silent too long":     {"active", now.Add(-time.Hour), now.Add(-5 * time.Minute), Offline},
		"joined, never heard": {"active", now.Add(-time.Hour), time.Time{}, Offline},
		"just joined":         {"active", now.Add(-5 * time.Second), time.Time{}, Online},
	} {
		if got := StatusOf(tc.state, tc.enrolled, tc.seen, now, after); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestSaveReportKeepsTheLatestCleanedAndSetsVersionAndLastSeen(t *testing.T) {
	service, st := newService(t)
	ctx := context.Background()
	if _, _, err := service.Report(ctx, "web01"); !errors.Is(err, ErrNoReport) {
		t.Fatalf("before any report: %v", err)
	}
	now := time.Now().Truncate(time.Second)
	first := agentapi.Report{Version: "1.0", Alerts: []agentapi.AlertStatus{{Key: "disk:/", Message: "full\x1b[31m"}}}
	if err := service.SaveReport(ctx, "web01", first, now); err != nil {
		t.Fatal(err)
	}
	if err := service.SaveReport(ctx, "web01", agentapi.Report{Version: "1.1"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	report, at, err := service.Report(ctx, "web01")
	if err != nil || report.Version != "1.1" || !at.Equal(now.Add(time.Minute)) {
		t.Errorf("report = %+v at %v, %v: want the second", report, at, err)
	}
	var version string
	var seen int64
	if err := st.DB.QueryRow(`SELECT version, last_seen FROM agents WHERE name = 'web01'`).Scan(&version, &seen); err != nil || version != "1.1" || seen != now.Add(time.Minute).Unix() {
		t.Errorf("agent row: %q %d, %v", version, seen, err)
	}
	if err := service.SaveReport(ctx, "web01", first, now); err != nil {
		t.Fatal(err)
	}
	report, _, _ = service.Report(ctx, "web01")
	if strings.ContainsRune(report.Alerts[0].Message, 0x1b) {
		t.Errorf("a control character was kept: %q", report.Alerts[0].Message)
	}
}

func TestSeenSetsLastSeen(t *testing.T) {
	service, st := newService(t)
	now := time.Now()
	if err := service.Seen(context.Background(), "web01", now); err != nil {
		t.Fatal(err)
	}
	var seen int64
	if err := st.DB.QueryRow(`SELECT last_seen FROM agents WHERE name = 'web01'`).Scan(&seen); err != nil || seen != now.Unix() {
		t.Errorf("last_seen = %d, %v", seen, err)
	}
}
