package uptime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel/internal/config"
)

func httpCheck(url string) config.UptimeCheck {
	return config.UptimeCheck{Name: "web", Type: config.CheckHTTP, URL: url, ExpectStatus: 200, Timeout: 2 * time.Second, FailAfter: 2}
}

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte("<h1>Welcome</h1>"))
		case "/slow":
			time.Sleep(500 * time.Millisecond)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()

	tests := []struct {
		name    string
		path    string
		keyword string
		timeout time.Duration // 0 keeps the default of httpCheck
		wantOK  bool
		wantErr string // a substring of the error
	}{
		{name: "ok", path: "/ok", wantOK: true},
		{name: "keyword found", path: "/ok", keyword: "Welcome", wantOK: true},
		{name: "keyword missing", path: "/ok", keyword: "Goodbye", wantErr: `keyword "Goodbye" not found`},
		{name: "unexpected status", path: "/broken", wantErr: "HTTP 502 (expected 200)"},
		{name: "timeout", path: "/slow", timeout: 100 * time.Millisecond, wantErr: "deadline exceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := httpCheck(srv.URL + tt.path)
			check.Keyword = tt.keyword
			if tt.timeout > 0 {
				check.Timeout = tt.timeout
			}
			probe := Probe(context.Background(), check)
			if probe.OK != tt.wantOK || !strings.Contains(probe.Error, tt.wantErr) {
				t.Errorf("probe = %+v, want OK %v and an error containing %q", probe, tt.wantOK, tt.wantErr)
			}
			if probe.Latency <= 0 {
				t.Errorf("latency = %v, want it measured", probe.Latency)
			}
		})
	}
}

// The error names the cause only: the alert already names the endpoint, so Go's `Get "url":` prefix is dropped.
func TestHTTPConnectionErrorHasNoURLPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	probe := Probe(context.Background(), httpCheck(url))
	if probe.OK || strings.HasPrefix(probe.Error, "Get ") || !strings.Contains(probe.Error, "refused") {
		t.Errorf("probe = %+v, want a refused connection without the Get prefix", probe)
	}
}

func TestProbeUnknownType(t *testing.T) {
	check := config.UptimeCheck{Name: "x", Type: "udp", Timeout: time.Second}
	probe := Probe(context.Background(), check)
	if probe.OK || probe.Error != `unknown check type "udp"` {
		t.Errorf("probe = %+v", probe)
	}
}

func TestHTTPSCertExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// Untrusted certificate: the check must fail.
	if probe := Probe(context.Background(), httpCheck(srv.URL)); probe.OK || !strings.Contains(probe.Error, "certificate") {
		t.Fatalf("untrusted cert = %+v", probe)
	}

	useTestTLS(t, srv)
	probe := Probe(context.Background(), httpCheck(srv.URL))
	if !probe.OK || probe.CertExpiry.IsZero() {
		t.Fatalf("trusted cert = %+v", probe)
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	check := config.UptimeCheck{Name: "db", Type: config.CheckTCP, Address: addr, Timeout: time.Second}
	if probe := Probe(context.Background(), check); !probe.OK {
		t.Errorf("open port = %+v", probe)
	}
	ln.Close()
	if probe := Probe(context.Background(), check); probe.OK || !strings.Contains(probe.Error, "refused") {
		t.Errorf("closed port = %+v", probe)
	}
}

func TestPingLocalhost(t *testing.T) {
	check := config.UptimeCheck{Name: "lo", Type: config.CheckPing, Host: "127.0.0.1", Timeout: 3 * time.Second}
	probe := Probe(context.Background(), check)
	if !probe.OK && strings.Contains(probe.Error, "CAP_NET_RAW") {
		t.Skip("ICMP not permitted in this environment:", probe.Error)
	}
	if !probe.OK {
		t.Errorf("ping localhost = %+v", probe)
	}
}

func TestProbeAllKeepsTheOrderOfTheChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closed.Addr().String()
	closed.Close()
	checks := []config.UptimeCheck{
		{Name: "up", Type: config.CheckTCP, Address: ln.Addr().String(), Timeout: time.Second},
		{Name: "down", Type: config.CheckTCP, Address: closedAddr, Timeout: time.Second},
	}
	probes := ProbeAll(context.Background(), checks)
	if len(probes) != 2 || !probes[0].OK || probes[1].OK {
		t.Fatalf("probes = %+v, want [up, down]", probes)
	}
}
