// Package uptime probes HTTP endpoints, TCP ports and hosts (ping). Turning
// failed probes into alerts is the daemon's job.
package uptime

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"sentinel/agent/internal/config"
)

// Result is the outcome of one probe.
type Result struct {
	OK      bool
	Latency time.Duration
	Error   string
	// CertExpiry is the leaf certificate's NotAfter for HTTPS checks.
	CertExpiry time.Time
}

// Probe runs one check, bounded by check.Timeout.
func Probe(ctx context.Context, check config.UptimeCheck) Result {
	ctx, cancel := context.WithTimeout(ctx, check.Timeout)
	defer cancel()
	startedAt := time.Now()
	var probe Result
	var err error
	switch check.Type {
	case config.CheckHTTP:
		probe.CertExpiry, err = probeHTTP(ctx, check)
	case config.CheckTCP:
		err = probeTCP(ctx, check.Address)
	case config.CheckPing:
		err = probePing(ctx, check.Host)
	default:
		err = fmt.Errorf("unknown check type %q", check.Type)
	}
	probe.Latency = time.Since(startedAt)
	probe.OK = err == nil
	if err != nil {
		probe.Error = err.Error()
	}
	return probe
}

// ProbeAll runs every check at once and returns the results in the order of checks.
func ProbeAll(ctx context.Context, checks []config.UptimeCheck) []Result {
	probes := make([]Result, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Go(func() { probes[i] = Probe(ctx, check) })
	}
	wg.Wait()
	return probes
}

// Endpoint is the human-readable address a check probes. An HTTP URL is shown without its user and
// password, query values and fragment: the secret of a health-check URL often sits there, and the
// endpoint is printed by `sentinel check` and sent in alerts to every notifier.
func Endpoint(check config.UptimeCheck) string {
	switch check.Type {
	case config.CheckHTTP:
		return redactURL(check.URL)
	case config.CheckTCP:
		return check.Address
	default:
		return check.Host
	}
}

// redactURL keeps the scheme, host, port and path and masks the query values.
func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)"
	}
	shown := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path, RawPath: parsed.RawPath}
	if parsed.RawQuery != "" {
		parts := strings.Split(parsed.RawQuery, "&")
		for i, part := range parts {
			if name, _, hasValue := strings.Cut(part, "="); hasValue {
				parts[i] = name + "=***"
			} else {
				parts[i] = "***"
			}
		}
		shown.RawQuery = strings.Join(parts, "&")
	}
	return shown.String()
}
