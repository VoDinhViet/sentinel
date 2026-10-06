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
	if r := Check(context.Background(), ok); !r.OK || r.Latency <= 0 {
		t.Errorf("ok check = %+v", r)
	}

	missing := httpCheck(srv.URL + "/ok")
	missing.Keyword = "Goodbye"
	if r := Check(context.Background(), missing); r.OK || !strings.Contains(r.Error, `keyword "Goodbye" not found`) {
		t.Errorf("keyword check = %+v", r)
	}

	if r := Check(context.Background(), httpCheck(srv.URL+"/broken")); r.OK || r.Error != "HTTP 502 (expected 200)" {
		t.Errorf("status check = %+v", r)
	}

	slow := httpCheck(srv.URL + "/slow")
	slow.Timeout = 100 * time.Millisecond
	if r := Check(context.Background(), slow); r.OK || !strings.Contains(r.Error, "deadline exceeded") {
		t.Errorf("timeout check = %+v", r)
	}
}

func TestHTTPSCertExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// Untrusted certificate: the check must fail.
	if r := Check(context.Background(), httpCheck(srv.URL)); r.OK || !strings.Contains(r.Error, "certificate") {
		t.Fatalf("untrusted cert = %+v", r)
	}

	orig := httpTransport.TLSClientConfig
	httpTransport.TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	defer func() { httpTransport.TLSClientConfig = orig }()

	r := Check(context.Background(), httpCheck(srv.URL))
	if !r.OK || r.CertExpiry.IsZero() {
		t.Fatalf("trusted cert = %+v", r)
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	c := config.UptimeCheck{Name: "db", Type: config.CheckTCP, Address: addr, Timeout: time.Second}
	if r := Check(context.Background(), c); !r.OK {
		t.Errorf("open port = %+v", r)
	}
	ln.Close()
	if r := Check(context.Background(), c); r.OK || !strings.Contains(r.Error, "refused") {
		t.Errorf("closed port = %+v", r)
	}
}

func TestPingLocalhost(t *testing.T) {
	c := config.UptimeCheck{Name: "lo", Type: config.CheckPing, Host: "127.0.0.1", Timeout: 3 * time.Second}
	r := Check(context.Background(), c)
	if !r.OK && strings.Contains(r.Error, "CAP_NET_RAW") {
		t.Skip("ICMP not permitted in this environment:", r.Error)
	}
	if !r.OK {
		t.Errorf("ping localhost = %+v", r)
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
	tr := &Tracker{Alerts: &alert.Manager{Sender: rec, Cooldown: time.Hour}}
	c := httpCheck("https://example.com") // FailAfter: 2
	down := Result{Error: "HTTP 502"}
	up := Result{OK: true, Latency: 20 * time.Millisecond}

	tr.Observe(ctx, c, down)
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("alerted after 1 failure: %v", got)
	}
	// A success in between resets the count.
	tr.Observe(ctx, c, up)
	tr.Observe(ctx, c, down)
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("count not reset by success: %v", got)
	}
	tr.Observe(ctx, c, down)
	if got := rec.take(); len(got) != 1 || got[0] != "CRITICAL web is DOWN" {
		t.Fatalf("after 2 failures: %v", got)
	}
	tr.Observe(ctx, c, up)
	if got := rec.take(); len(got) != 1 || got[0] != "RESOLVED web is UP" {
		t.Fatalf("recovery: %v", got)
	}
}

func TestTrackerCertExpiry(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	tr := &Tracker{Alerts: &alert.Manager{Sender: rec}, Now: func() time.Time { return now }}
	c := httpCheck("https://example.com")
	c.CertExpiryDays = 14

	tr.Observe(ctx, c, Result{OK: true, CertExpiry: now.Add(30 * 24 * time.Hour)})
	if got := rec.take(); len(got) != 0 {
		t.Fatalf("valid cert alerted: %v", got)
	}
	tr.Observe(ctx, c, Result{OK: true, CertExpiry: now.Add(5 * 24 * time.Hour)})
	if got := rec.take(); len(got) != 1 || got[0] != "WARNING TLS certificate for web expires soon" {
		t.Fatalf("expiring cert: %v", got)
	}
	tr.Observe(ctx, c, Result{OK: true, CertExpiry: now.Add(90 * 24 * time.Hour)})
	if got := rec.take(); len(got) != 1 || !strings.HasPrefix(got[0], "RESOLVED") {
		t.Fatalf("renewed cert: %v", got)
	}
}
