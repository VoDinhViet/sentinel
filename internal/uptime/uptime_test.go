package uptime

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel/internal/alert"
	"sentinel/internal/config"
	"sentinel/internal/notify"
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

	ok := httpCheck(srv.URL + "/ok")
	ok.Keyword = "Welcome"
	if result := Probe(context.Background(), ok); !result.OK || result.Latency <= 0 {
		t.Errorf("ok check = %+v", result)
	}

	missing := httpCheck(srv.URL + "/ok")
	missing.Keyword = "Goodbye"
	if result := Probe(context.Background(), missing); result.OK || !strings.Contains(result.Error, `keyword "Goodbye" not found`) {
		t.Errorf("keyword check = %+v", result)
	}

	if result := Probe(context.Background(), httpCheck(srv.URL+"/broken")); result.OK || result.Error != "HTTP 502 (expected 200)" {
		t.Errorf("status check = %+v", result)
	}

	slow := httpCheck(srv.URL + "/slow")
	slow.Timeout = 100 * time.Millisecond
	if result := Probe(context.Background(), slow); result.OK || !strings.Contains(result.Error, "deadline exceeded") {
		t.Errorf("timeout check = %+v", result)
	}
}

func TestHTTPSCertExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// Untrusted certificate: the check must fail.
	if result := Probe(context.Background(), httpCheck(srv.URL)); result.OK || !strings.Contains(result.Error, "certificate") {
		t.Fatalf("untrusted cert = %+v", result)
	}

	orig := httpTransport.TLSClientConfig
	httpTransport.TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	defer func() { httpTransport.TLSClientConfig = orig }()

	result := Probe(context.Background(), httpCheck(srv.URL))
	if !result.OK || result.CertExpiry.IsZero() {
		t.Fatalf("trusted cert = %+v", result)
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	check := config.UptimeCheck{Name: "db", Type: config.CheckTCP, Address: addr, Timeout: time.Second}
	if result := Probe(context.Background(), check); !result.OK {
		t.Errorf("open port = %+v", result)
	}
	ln.Close()
	if result := Probe(context.Background(), check); result.OK || !strings.Contains(result.Error, "refused") {
		t.Errorf("closed port = %+v", result)
	}
}

func TestPingLocalhost(t *testing.T) {
	check := config.UptimeCheck{Name: "lo", Type: config.CheckPing, Host: "127.0.0.1", Timeout: 3 * time.Second}
	result := Probe(context.Background(), check)
	if !result.OK && strings.Contains(result.Error, "CAP_NET_RAW") {
		t.Skip("ICMP not permitted in this environment:", result.Error)
	}
	if !result.OK {
		t.Errorf("ping localhost = %+v", result)
	}
}

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

func TestTrackerFailAfter(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	tr := &Tracker{Alerts: &alert.Manager{Notifier: rec, Cooldown: time.Hour}}
	check := httpCheck("https://example.com") // FailAfter: 2
	down := Result{Error: "HTTP 502"}
	up := Result{OK: true, Latency: 20 * time.Millisecond}

	tr.Observe(ctx, check, down)
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("alerted after 1 failure: %v", got)
	}
	// A success in between resets the count.
	tr.Observe(ctx, check, up)
	tr.Observe(ctx, check, down)
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("count not reset by success: %v", got)
	}
	tr.Observe(ctx, check, down)
	if got := rec.take(); len(got) != 1 || got[0] != "CRITICAL web is DOWN" {
		t.Fatalf("after 2 failures: %v", got)
	}
	tr.Observe(ctx, check, up)
	if got := rec.take(); len(got) != 1 || got[0] != "RESOLVED web is UP" {
		t.Fatalf("recovery: %v", got)
	}
}

func TestTrackerCertExpiry(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	tr := &Tracker{Alerts: &alert.Manager{Notifier: rec}, Now: func() time.Time { return now }}
	check := httpCheck("https://example.com")
	check.CertExpiryDays = 14

	tr.Observe(ctx, check, Result{OK: true, CertExpiry: now.Add(30 * 24 * time.Hour)})
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("valid cert alerted: %v", got)
	}
	tr.Observe(ctx, check, Result{OK: true, CertExpiry: now.Add(5 * 24 * time.Hour)})
	if got := rec.take(); len(got) != 1 || got[0] != "WARNING TLS certificate for web expires soon" {
		t.Fatalf("expiring cert: %v", got)
	}
	tr.Observe(ctx, check, Result{OK: true, CertExpiry: now.Add(90 * 24 * time.Hour)})
	if got := rec.take(); len(got) != 1 || !strings.HasPrefix(got[0], "RESOLVED") {
		t.Fatalf("renewed cert: %v", got)
	}
}
