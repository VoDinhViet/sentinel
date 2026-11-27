package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sentinel/agent/internal/alertkey"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/store"
	"sentinel/agent/internal/uptime"
	"sentinel/pkg/alert"
	"sentinel/pkg/notify"
)

// UptimeTracker turns individual probe results into alerts once a check has
// failed FailAfter times in a row, and resolves them on the first success.
type UptimeTracker struct {
	Alerts *alert.Manager
	Store  *store.Store // optional
	Now    func() time.Time

	mu             sync.Mutex
	failuresByName map[string]int
}

func (t *UptimeTracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Observe takes one probe of check: it records it, then raises or resolves the DOWN alert and,
// for an HTTPS check that watches its certificate, the expiry alert.
func (t *UptimeTracker) Observe(ctx context.Context, check config.UptimeCheck, probe uptime.Result) {
	now := t.now()
	t.record(ctx, check, probe, now)
	failures := t.countFailures(check.Name, probe.OK)
	t.alertUptime(ctx, check, probe, failures)
	t.alertCert(ctx, check, probe, now)
}

func (t *UptimeTracker) record(ctx context.Context, check config.UptimeCheck, probe uptime.Result, now time.Time) {
	if t.Store == nil {
		return
	}
	err := t.Store.RecordCheck(ctx, store.CheckResult{Name: check.Name, At: now, OK: probe.OK, Latency: probe.Latency, Error: probe.Error})
	if err != nil {
		slog.Error("record check", "check", check.Name, "error", err)
	}
}

// countFailures returns how many probes of the check in a row have failed, counting this one.
func (t *UptimeTracker) countFailures(name string, ok bool) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failuresByName == nil {
		t.failuresByName = map[string]int{}
	}
	if ok {
		t.failuresByName[name] = 0
	} else {
		t.failuresByName[name]++
	}
	return t.failuresByName[name]
}

func (t *UptimeTracker) alertUptime(ctx context.Context, check config.UptimeCheck, probe uptime.Result, failures int) {
	key := alertkey.Uptime(check.Name)
	endpoint := uptime.Endpoint(check)
	switch {
	case probe.OK:
		t.Alerts.Resolve(ctx, key, check.Name+" is UP", fmt.Sprintf("%s responded in %s.", endpoint, probe.Latency.Round(time.Millisecond)))
	case failures >= check.FailAfter:
		t.Alerts.Fire(ctx, key, notify.Critical, check.Name+" is DOWN",
			fmt.Sprintf("%s: %s (failed %d checks in a row)", endpoint, probe.Error, failures))
	default:
		slog.Warn("check failed", "check", check.Name, "error", probe.Error, "consecutive", failures, "alert_after", check.FailAfter)
	}
}

func (t *UptimeTracker) alertCert(ctx context.Context, check config.UptimeCheck, probe uptime.Result, now time.Time) {
	if check.CertExpiryDays <= 0 || probe.CertExpiry.IsZero() {
		return
	}
	untilExpiry := probe.CertExpiry.Sub(now)
	key := alertkey.Cert(check.Name)
	if untilExpiry < time.Duration(check.CertExpiryDays)*24*time.Hour {
		t.Alerts.Fire(ctx, key, notify.Warning, "TLS certificate for "+check.Name+" expires soon",
			fmt.Sprintf("%s: certificate expires %s (in %d days).", uptime.Endpoint(check), probe.CertExpiry.Format(time.DateOnly), int(untilExpiry.Hours()/24)))
		return
	}
	t.Alerts.Resolve(ctx, key, "TLS certificate for "+check.Name+" renewed",
		fmt.Sprintf("Now valid until %s.", probe.CertExpiry.Format(time.DateOnly)))
}

// uptimeTasks returns one interval task per uptime check: probe it, then let the tracker
// record the result and raise or resolve its alert.
func uptimeTasks(cfg *config.Config, st *store.Store, alerts *alert.Manager) []Task {
	var tasks []Task
	tracker := &UptimeTracker{Alerts: alerts, Store: st}
	for _, check := range cfg.UptimeChecks {
		tasks = append(tasks, Task{
			Name:  "uptime:" + check.Name,
			Every: check.Interval,
			Run: func(ctx context.Context) {
				tracker.Observe(ctx, check, uptime.Probe(ctx, check))
			},
		})
	}
	return tasks
}
