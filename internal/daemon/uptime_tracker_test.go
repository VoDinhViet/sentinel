package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"sentinel/internal/alert"
	"sentinel/internal/config"
	"sentinel/internal/uptime"
)

func httpCheck(url string) config.UptimeCheck {
	return config.UptimeCheck{Name: "web", Type: config.CheckHTTP, URL: url, ExpectStatus: 200, Timeout: 2 * time.Second, FailAfter: 2}
}

func TestUptimeTrackerFailAfter(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	tr := &UptimeTracker{Alerts: &alert.Manager{Notifier: rec, Cooldown: time.Hour}}
	check := httpCheck("https://example.com") // FailAfter: 2
	down := uptime.Result{Error: "HTTP 502"}
	up := uptime.Result{OK: true, Latency: 20 * time.Millisecond}

	tr.Observe(ctx, check, down)
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("alerted after 1 failure: %v", got)
	}
	// A success in between resets the count.
	tr.Observe(ctx, check, up)
	tr.Observe(ctx, check, down)
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("count not reset by success: %v", got)
	}
	tr.Observe(ctx, check, down)
	if got := rec.titles(); len(got) != 1 || got[0] != "CRITICAL web is DOWN" {
		t.Fatalf("after 2 failures: %v", got)
	}
	tr.Observe(ctx, check, up)
	if got := rec.titles(); len(got) != 1 || got[0] != "RESOLVED web is UP" {
		t.Fatalf("recovery: %v", got)
	}
}

func TestUptimeTrackerCertExpiry(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	tr := &UptimeTracker{Alerts: &alert.Manager{Notifier: rec}, Now: func() time.Time { return now }}
	check := httpCheck("https://example.com")
	check.CertExpiryDays = 14

	tr.Observe(ctx, check, uptime.Result{OK: true, CertExpiry: now.Add(30 * 24 * time.Hour)})
	if got := rec.titles(); len(got) != 0 {
		t.Fatalf("valid cert alerted: %v", got)
	}
	tr.Observe(ctx, check, uptime.Result{OK: true, CertExpiry: now.Add(5 * 24 * time.Hour)})
	if got := rec.titles(); len(got) != 1 || got[0] != "WARNING TLS certificate for web expires soon" {
		t.Fatalf("expiring cert: %v", got)
	}
	tr.Observe(ctx, check, uptime.Result{OK: true, CertExpiry: now.Add(90 * 24 * time.Hour)})
	if got := rec.titles(); len(got) != 1 || !strings.HasPrefix(got[0], "RESOLVED") {
		t.Fatalf("renewed cert: %v", got)
	}
}
