// Package uptime probes HTTP endpoints, TCP ports and hosts (ping). Turning
// failed probes into alerts is the daemon's job.
package uptime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"sentinel/internal/config"
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

// ProbeAll runs every check at once and returns the results in the order of checks.
func ProbeAll(ctx context.Context, checks []config.UptimeCheck) []Result {
	results := make([]Result, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = Probe(ctx, check)
		}()
	}
	wg.Wait()
	return results
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
