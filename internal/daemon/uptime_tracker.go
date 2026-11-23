package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sentinel/internal/alert"
	"sentinel/internal/config"
	"sentinel/internal/notify"
	"sentinel/internal/store"
	"sentinel/internal/uptime"
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

func (t *UptimeTracker) Observe(ctx context.Context, check config.UptimeCheck, result uptime.Result) {
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
	endpoint := uptime.Endpoint(check)
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
