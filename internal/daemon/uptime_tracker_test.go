package daemon

import (
	"context"
	"slices"
	"testing"
	"time"

	"sentinel/internal/alert"
	"sentinel/internal/config"
	"sentinel/internal/uptime"
)

func webCheck() config.UptimeCheck {
	return config.UptimeCheck{Name: "web", Type: config.CheckHTTP, URL: "https://example.com", ExpectStatus: 200, Timeout: 2 * time.Second, FailAfter: 2}
}

// observation is one probe handed to the tracker and the titles of the alerts it must send.
type observation struct {
	name       string
	probe      uptime.Result
	wantTitles []string
}

// observeAll feeds the probes to one tracker in order and checks the alerts after each.
func observeAll(t *testing.T, tracker *UptimeTracker, rec *recorder, check config.UptimeCheck, observations []observation) {
	t.Helper()
	for _, observed := range observations {
		tracker.Observe(context.Background(), check, observed.probe)
		if got := rec.titles(); !slices.Equal(got, observed.wantTitles) {
			t.Fatalf("%s: alerts %v, want %v", observed.name, got, observed.wantTitles)
		}
	}
}

func TestUptimeTrackerFailAfter(t *testing.T) {
	rec := &recorder{}
	tracker := &UptimeTracker{Alerts: &alert.Manager{Notifier: rec, Cooldown: time.Hour}}
	down := uptime.Result{Error: "HTTP 502"}
	up := uptime.Result{OK: true, Latency: 20 * time.Millisecond}

	observeAll(t, tracker, rec, webCheck(), []observation{ // FailAfter: 2
		{name: "first failure", probe: down},
		{name: "a success resets the count", probe: up},
		{name: "first failure again", probe: down},
		{name: "second failure in a row", probe: down, wantTitles: []string{"CRITICAL web is DOWN"}},
		{name: "recovery", probe: up, wantTitles: []string{"RESOLVED web is UP"}},
	})
}

func TestUptimeTrackerCertExpiry(t *testing.T) {
	rec := &recorder{}
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	tracker := &UptimeTracker{Alerts: &alert.Manager{Notifier: rec}, Now: func() time.Time { return now }}
	check := webCheck()
	check.CertExpiryDays = 14
	certExpiringIn := func(days int) uptime.Result {
		return uptime.Result{OK: true, CertExpiry: now.Add(time.Duration(days) * 24 * time.Hour)}
	}

	observeAll(t, tracker, rec, check, []observation{
		{name: "valid certificate", probe: certExpiringIn(30)},
		{name: "expiring certificate", probe: certExpiringIn(5), wantTitles: []string{"WARNING TLS certificate for web expires soon"}},
		{name: "renewed certificate", probe: certExpiringIn(90), wantTitles: []string{"RESOLVED TLS certificate for web renewed"}},
	})
}
