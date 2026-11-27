package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/uptime"
)

func TestNewProbeOutcome(t *testing.T) {
	expiry := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		check         config.UptimeCheck
		probe         uptime.Result
		wantEndpoint  string
		wantLatencyMs int64
		wantCert      bool
	}{
		{
			name:          "latency is rounded to the millisecond, not cut",
			check:         config.UptimeCheck{Name: "db", Type: config.CheckTCP, Address: "db:5432"},
			probe:         uptime.Result{OK: true, Latency: 1600 * time.Microsecond},
			wantEndpoint:  "db:5432",
			wantLatencyMs: 2,
		},
		{
			name:          "an https check carries the certificate expiry",
			check:         config.UptimeCheck{Name: "web", Type: config.CheckHTTP, URL: "https://example.com/health"},
			probe:         uptime.Result{OK: true, Latency: 40 * time.Millisecond, CertExpiry: expiry},
			wantEndpoint:  "https://example.com/health",
			wantLatencyMs: 40,
			wantCert:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := newProbeOutcome(tt.check, tt.probe)
			if outcome.Name != tt.check.Name || outcome.Endpoint != tt.wantEndpoint || outcome.LatencyMs != tt.wantLatencyMs {
				t.Errorf("outcome = %+v", outcome)
			}
			if (outcome.CertExpiry != nil) != tt.wantCert {
				t.Errorf("CertExpiry = %v, want set: %v", outcome.CertExpiry, tt.wantCert)
			}
		})
	}
}

func TestPrintOutcomes(t *testing.T) {
	expiry := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	outcomes := []probeOutcome{
		{Name: "api", Endpoint: "https://api.example.com", OK: true, LatencyMs: 12},
		{Name: "db", Endpoint: "db:5432", LatencyMs: 2000, Error: "connection refused"},
		{Name: "web", Endpoint: "https://example.com", OK: true, LatencyMs: 80, CertExpiry: &expiry},
		{Name: "shop", Endpoint: "https://shop.example.com", LatencyMs: 5, Error: "HTTP 502 (expected 200)", CertExpiry: &expiry},
	}
	want := []string{
		"NAME ENDPOINT STATUS LATENCY DETAIL",
		"api https://api.example.com UP 12ms",
		"db db:5432 DOWN 2s connection refused",
		"web https://example.com UP 80ms cert expires 2026-12-01",
		"shop https://shop.example.com DOWN 5ms HTTP 502 (expected 200) cert expires 2026-12-01",
	}

	var out bytes.Buffer
	if err := printOutcomes(&out, outcomes); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), out.String())
	}
	for i, line := range lines {
		if got := strings.Join(strings.Fields(line), " "); got != want[i] {
			t.Errorf("line %d = %q, want %q", i, got, want[i])
		}
	}
}
