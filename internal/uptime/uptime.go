// Package uptime probes HTTP endpoints, TCP ports and hosts (ping) and turns
// consecutive failures into alerts.
package uptime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"sentinel/internal/alert"
	"sentinel/internal/config"
	"sentinel/internal/notify"
	"sentinel/internal/store"
)

type Result struct {
	OK      bool
	Latency time.Duration
	Error   string
	// CertExpiry is the leaf certificate's NotAfter for HTTPS checks.
	CertExpiry time.Time
}

// maxBody bounds how much of a response is searched for the keyword.
const maxBody = 1 << 20

// Check runs one probe, bounded by c.Timeout.
func Check(ctx context.Context, c config.UptimeCheck) Result {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	start := time.Now()
	var r Result
	var err error
	switch c.Type {
	case config.CheckHTTP:
		r, err = checkHTTP(ctx, c)
	case config.CheckTCP:
		err = checkTCP(ctx, c.Address)
	case config.CheckPing:
		err = checkPing(ctx, c.Host)
	default:
		err = fmt.Errorf("unknown check type %q", c.Type)
	}
	r.Latency = time.Since(start)
	if err != nil {
		r.OK, r.Error = false, err.Error()
	} else {
		r.OK = true
	}
	return r
}

var httpTransport = &http.Transport{
	Proxy:             http.ProxyFromEnvironment,
	DisableKeepAlives: true, // measure a fresh connection every time
}

func checkHTTP(ctx context.Context, c config.UptimeCheck) (Result, error) {
	var r Result
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return r, err
	}
	req.Header.Set("User-Agent", "sentinel-uptime/1")
	resp, err := (&http.Client{Transport: httpTransport}).Do(req)
	if err != nil {
		return r, simplifyErr(err)
	}
	defer resp.Body.Close()
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		r.CertExpiry = resp.TLS.PeerCertificates[0].NotAfter
	}
	if resp.StatusCode != c.ExpectStatus {
		return r, fmt.Errorf("HTTP %d (expected %d)", resp.StatusCode, c.ExpectStatus)
	}
	if c.Keyword != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return r, fmt.Errorf("read body: %w", err)
		}
		if !strings.Contains(string(body), c.Keyword) {
			return r, fmt.Errorf("keyword %q not found in response", c.Keyword)
		}
	}
	return r, nil
}

// simplifyErr strips Go's `Get "url":` prefix, which the alert already names.
func simplifyErr(err error) error {
	var uerr interface{ Unwrap() error }
	if errors.As(err, &uerr) && strings.HasPrefix(err.Error(), "Get ") {
		if inner := uerr.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

func checkTCP(ctx context.Context, addr string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

func checkPing(ctx context.Context, host string) error {
	p, err := probing.NewPinger(host)
	if err != nil {
		return err
	}
	p.Count = 3
	p.Interval = 300 * time.Millisecond
	if dl, ok := ctx.Deadline(); ok {
		p.Timeout = time.Until(dl)
	}
	// Raw ICMP needs CAP_NET_RAW (granted by the systemd unit); fall back to
	// unprivileged ICMP datagram sockets otherwise.
	p.SetPrivileged(true)
	if err := p.RunWithContext(ctx); err != nil {
		p, _ = probing.NewPinger(host)
		p.Count, p.Interval = 3, 300*time.Millisecond
		if dl, ok := ctx.Deadline(); ok {
			p.Timeout = time.Until(dl)
		}
		p.SetPrivileged(false)
		if err := p.RunWithContext(ctx); err != nil {
			return fmt.Errorf("ping: %w (grant CAP_NET_RAW or allow unprivileged ping)", err)
		}
	}
	if s := p.Statistics(); s.PacketsRecv == 0 {
		return fmt.Errorf("no reply from %s (%d packets sent)", host, s.PacketsSent)
	}
	return nil
}

// Tracker turns individual results into alerts once a check has failed
// FailAfter times in a row, and resolves them on the first success.
type Tracker struct {
	Alerts *alert.Manager
	Store  *store.Store // optional
	Now    func() time.Time

	mu       sync.Mutex
	failures map[string]int
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tracker) Observe(ctx context.Context, c config.UptimeCheck, r Result) {
	now := t.now()
	if t.Store != nil {
		err := t.Store.RecordCheck(ctx, store.CheckResult{Name: c.Name, At: now, OK: r.OK, Latency: r.Latency, Error: r.Error})
		if err != nil {
			slog.Error("record check", "check", c.Name, "error", err)
		}
	}

	t.mu.Lock()
	if t.failures == nil {
		t.failures = map[string]int{}
	}
	if r.OK {
		t.failures[c.Name] = 0
	} else {
		t.failures[c.Name]++
	}
	fails := t.failures[c.Name]
	t.mu.Unlock()

	key := "uptime:" + c.Name
	target := Target(c)
	switch {
	case r.OK:
		t.Alerts.Resolve(ctx, key, c.Name+" is UP", fmt.Sprintf("%s responded in %s.", target, r.Latency.Round(time.Millisecond)))
	case fails >= c.FailAfter:
		t.Alerts.Fire(ctx, key, notify.Critical, c.Name+" is DOWN",
			fmt.Sprintf("%s: %s (failed %d checks in a row)", target, r.Error, fails))
	default:
		slog.Warn("check failed", "check", c.Name, "error", r.Error, "consecutive", fails, "alert_after", c.FailAfter)
	}

	if c.CertExpiryDays > 0 && !r.CertExpiry.IsZero() {
		left := r.CertExpiry.Sub(now)
		certKey := "cert:" + c.Name
		if left < time.Duration(c.CertExpiryDays)*24*time.Hour {
			t.Alerts.Fire(ctx, certKey, notify.Warning, "TLS certificate for "+c.Name+" expires soon",
				fmt.Sprintf("%s: certificate expires %s (in %d days).", target, r.CertExpiry.Format("2006-01-02"), int(left.Hours()/24)))
		} else {
			t.Alerts.Resolve(ctx, certKey, "TLS certificate for "+c.Name+" renewed",
				fmt.Sprintf("Now valid until %s.", r.CertExpiry.Format("2006-01-02")))
		}
	}
}

// Target is the human-readable address a check probes.
func Target(c config.UptimeCheck) string {
	switch c.Type {
	case config.CheckHTTP:
		return c.URL
	case config.CheckTCP:
		return c.Address
	default:
		return c.Host
	}
}
