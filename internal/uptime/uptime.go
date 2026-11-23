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

// Probe runs one check, bounded by check.Timeout.
func Probe(ctx context.Context, check config.UptimeCheck) Result {
	ctx, cancel := context.WithTimeout(ctx, check.Timeout)
	defer cancel()
	startedAt := time.Now()
	var result Result
	var err error
	switch check.Type {
	case config.CheckHTTP:
		result, err = probeHTTP(ctx, check)
	case config.CheckTCP:
		err = probeTCP(ctx, check.Address)
	case config.CheckPing:
		err = probePing(ctx, check.Host)
	default:
		err = fmt.Errorf("unknown check type %q", check.Type)
	}
	result.Latency = time.Since(startedAt)
	if err != nil {
		result.OK, result.Error = false, err.Error()
	} else {
		result.OK = true
	}
	return result
}

var httpTransport = &http.Transport{
	Proxy:             http.ProxyFromEnvironment,
	DisableKeepAlives: true, // measure a fresh connection every time
}

func probeHTTP(ctx context.Context, check config.UptimeCheck) (Result, error) {
	var result Result
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, check.URL, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("User-Agent", "sentinel-uptime/1")
	resp, err := (&http.Client{Transport: httpTransport}).Do(req)
	if err != nil {
		return result, simplifyErr(err)
	}
	defer resp.Body.Close()
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		result.CertExpiry = resp.TLS.PeerCertificates[0].NotAfter
	}
	if resp.StatusCode != check.ExpectStatus {
		return result, fmt.Errorf("HTTP %d (expected %d)", resp.StatusCode, check.ExpectStatus)
	}
	if check.Keyword != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return result, fmt.Errorf("read body: %w", err)
		}
		if !strings.Contains(string(body), check.Keyword) {
			return result, fmt.Errorf("keyword %q not found in response", check.Keyword)
		}
	}
	return result, nil
}

// simplifyErr strips Go's `Get "url":` prefix, which the alert already names.
func simplifyErr(err error) error {
	var wrapper interface{ Unwrap() error }
	if errors.As(err, &wrapper) && strings.HasPrefix(err.Error(), "Get ") {
		if inner := wrapper.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

func probeTCP(ctx context.Context, address string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	return conn.Close()
}

func probePing(ctx context.Context, host string) error {
	pinger, err := probing.NewPinger(host)
	if err != nil {
		return err
	}
	pinger.Count = 3
	pinger.Interval = 300 * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok {
		pinger.Timeout = time.Until(deadline)
	}
	// Raw ICMP needs CAP_NET_RAW (granted by the systemd unit); fall back to
	// unprivileged ICMP datagram sockets otherwise.
	pinger.SetPrivileged(true)
	if err := pinger.RunWithContext(ctx); err != nil {
		pinger, _ = probing.NewPinger(host)
		pinger.Count, pinger.Interval = 3, 300*time.Millisecond
		if deadline, ok := ctx.Deadline(); ok {
			pinger.Timeout = time.Until(deadline)
		}
		pinger.SetPrivileged(false)
		if err := pinger.RunWithContext(ctx); err != nil {
			return fmt.Errorf("ping: %w (grant CAP_NET_RAW or allow unprivileged ping)", err)
		}
	}
	if stats := pinger.Statistics(); stats.PacketsRecv == 0 {
		return fmt.Errorf("no reply from %s (%d packets sent)", host, stats.PacketsSent)
	}
	return nil
}

// Tracker turns individual results into alerts once a check has failed
// FailAfter times in a row, and resolves them on the first success.
type Tracker struct {
	Alerts *alert.Manager
	Store  *store.Store // optional
	Now    func() time.Time

	mu             sync.Mutex
	failuresByName map[string]int
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tracker) Observe(ctx context.Context, check config.UptimeCheck, result Result) {
	now := t.now()
	if t.Store != nil {
		err := t.Store.RecordCheck(ctx, store.CheckResult{Name: check.Name, At: now, OK: result.OK, Latency: result.Latency, Error: result.Error})
		if err != nil {
			slog.Error("record check", "check", check.Name, "error", err)
		}
	}

	t.mu.Lock()
	if t.failuresByName == nil {
		t.failuresByName = map[string]int{}
	}
	if result.OK {
		t.failuresByName[check.Name] = 0
	} else {
		t.failuresByName[check.Name]++
	}
	failures := t.failuresByName[check.Name]
	t.mu.Unlock()

	key := "uptime:" + check.Name
	endpoint := Endpoint(check)
	switch {
	case result.OK:
		t.Alerts.Resolve(ctx, key, check.Name+" is UP", fmt.Sprintf("%s responded in %s.", endpoint, result.Latency.Round(time.Millisecond)))
	case failures >= check.FailAfter:
		t.Alerts.Fire(ctx, key, notify.Critical, check.Name+" is DOWN",
			fmt.Sprintf("%s: %s (failed %d checks in a row)", endpoint, result.Error, failures))
	default:
		slog.Warn("check failed", "check", check.Name, "error", result.Error, "consecutive", failures, "alert_after", check.FailAfter)
	}

	if check.CertExpiryDays > 0 && !result.CertExpiry.IsZero() {
		untilExpiry := result.CertExpiry.Sub(now)
		certKey := "cert:" + check.Name
		if untilExpiry < time.Duration(check.CertExpiryDays)*24*time.Hour {
			t.Alerts.Fire(ctx, certKey, notify.Warning, "TLS certificate for "+check.Name+" expires soon",
				fmt.Sprintf("%s: certificate expires %s (in %d days).", endpoint, result.CertExpiry.Format("2006-01-02"), int(untilExpiry.Hours()/24)))
		} else {
			t.Alerts.Resolve(ctx, certKey, "TLS certificate for "+check.Name+" renewed",
				fmt.Sprintf("Now valid until %s.", result.CertExpiry.Format("2006-01-02")))
		}
	}
}

// Endpoint is the human-readable address a check probes.
func Endpoint(check config.UptimeCheck) string {
	switch check.Type {
	case config.CheckHTTP:
		return check.URL
	case config.CheckTCP:
		return check.Address
	default:
		return check.Host
	}
}
