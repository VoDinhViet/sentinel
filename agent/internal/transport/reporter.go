package transport

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"sentinel/pkg/agentapi"
)

// What the reporter does about time.
const (
	// RenewBefore is how close to its end the certificate is when the agent asks for a new one. The
	// control plane allows it from 45 days, and warns about an agent that still has the old one at 14.
	RenewBefore = 30 * 24 * time.Hour
	// renewRetry is how long to wait after a renewal that failed. It is the shortest time the control plane
	// allows between two certificates, and the certificate has 30 days left when the first try is made.
	// retryAfterFailure is how soon a report that did not get through is tried again: sooner than the
	// pace, so that one lost request does not make the control plane wait for two periods.
	retryAfterFailure = 20 * time.Second
	renewRetry        = time.Hour
	// refusedPause is how long the reporter stays quiet after the control plane refused its certificate
	// (revoked, or ended): it does not hammer, and it notices an agent that was put back.
	refusedPause = time.Hour
	// maxSkew is how far the clocks of this machine and the control plane may differ before it is said:
	// a certificate is checked against both clocks.
	maxSkew = 2 * time.Minute
	// The control plane sets the pace, within these bounds.
	minEvery = 15 * time.Second
	maxEvery = 10 * time.Minute
)

// Sender is the part of a Client the reporter uses.
type Sender interface {
	Report(ctx context.Context, report agentapi.Report) (agentapi.ReportResponse, error)
	Renew(ctx context.Context) (time.Time, error)
	NotAfter() time.Time
	// Reload reads the identity from its files again, and Ping is a request that proves the agent is alive.
	Reload() error
	Ping(ctx context.Context) error
}

// Reporter tells the control plane how this agent is, every minute or so, and renews the certificate
// when it is near its end. It never stops the daemon: an agent whose control plane is gone still backs
// up and alerts on its own.
type Reporter struct {
	Client Sender
	// Build makes the report of the moment: the host, the backups, the checks, the open alerts.
	Build func(ctx context.Context) (agentapi.Report, error)
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	every        time.Duration // the pace the control plane asked for last
	failing      bool          // the last report did not get through
	refusedUntil time.Time
	lastRenewTry time.Time
	wasRefused   bool // the last pause was for a refusal: the identity is read again before the next try
	skewWarned   bool
}

func (r *Reporter) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Loop reports until ctx is done: once now, then after the pace the control plane asks for, give or take
// 10% so a fleet that started together does not report together.
func (r *Reporter) Loop(ctx context.Context) {
	for {
		wait := r.pace()
		if !r.Round(ctx) {
			wait = min(wait, retryAfterFailure)
		}
		timer := time.NewTimer(jitter(wait))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (r *Reporter) pace() time.Duration {
	if r.every == 0 {
		return time.Duration(agentapi.DefaultReportGap) * time.Second
	}
	return r.every
}

func jitter(every time.Duration) time.Duration {
	return time.Duration(float64(every) * (0.9 + 0.2*rand.Float64())) //nolint:gosec // spreading load, not a secret
}

// Round is one report, after the renewal of the certificate when it is due. It says whether the control
// plane was reached (a refused certificate is not a failure to retry soon: the pause is an hour).
func (r *Reporter) Round(ctx context.Context) (reached bool) {
	now := r.now()
	if now.Before(r.refusedUntil) {
		return true
	}
	if r.wasRefused {
		r.wasRefused = false
		r.reloadIdentity()
	}
	r.renewIfDue(ctx, now)
	if now.Before(r.refusedUntil) {
		return true
	}
	report, err := r.Build(ctx)
	if err != nil {
		slog.Warn("control plane: cannot build the report; telling it only that this agent is alive", "err", err)
		return r.ping(ctx)
	}
	report.SentAt = now
	answer, err := r.Client.Report(ctx, report)
	switch {
	case ctx.Err() != nil:
		return true // the daemon is stopping
	case IsStatus(err, 401):
		r.refused(now, err)
		return true
	case err != nil:
		if !r.failing {
			slog.Warn("control plane: the report did not get through; it is tried again", "err", err)
		}
		r.failing = true
		return false
	}
	if r.failing {
		slog.Info("control plane: reporting again")
	}
	r.failing = false
	if answer.ReportEverySeconds > 0 { // a control plane that says nothing leaves the pace as it was
		r.every = min(max(time.Duration(answer.ReportEverySeconds)*time.Second, minEvery), maxEvery)
	}
	r.checkClock(answer.ServerTime, now)
	return true
}

// ping is the round of an agent that could not build a report.
func (r *Reporter) ping(ctx context.Context) bool {
	err := r.Client.Ping(ctx)
	if IsStatus(err, 401) {
		r.refused(r.now(), err)
		return true
	}
	return err == nil || ctx.Err() != nil
}

// reloadIdentity reads the identity files again after a refusal, so an agent that was joined again is taken
// back without a restart.
func (r *Reporter) reloadIdentity() {
	if err := r.Client.Reload(); err != nil {
		slog.Warn("control plane: cannot read the identity again", "err", err)
	}
}

func (r *Reporter) refused(now time.Time, err error) {
	r.refusedUntil = now.Add(refusedPause)
	r.wasRefused = true
	r.failing = true
	slog.Error("control plane: it refuses this agent's certificate (the server was revoked, or its certificate ended); "+
		"it asks again in an hour, with the identity it finds then. To join again: sentinelctl server add, then sentinel agent join --force", "err", err)
}

func (r *Reporter) renewIfDue(ctx context.Context, now time.Time) {
	notAfter := r.Client.NotAfter()
	if notAfter.IsZero() || notAfter.Sub(now) > RenewBefore || now.Sub(r.lastRenewTry) < renewRetry {
		return
	}
	r.lastRenewTry = now
	renewed, err := r.Client.Renew(ctx)
	switch {
	case ctx.Err() != nil:
	case IsStatus(err, 401):
		r.refused(now, err)
	case err != nil:
		slog.Warn("control plane: cannot renew the certificate yet; it is tried again", "ends", notAfter.Format("2006-01-02"), "err", err)
	default:
		slog.Info("control plane: the certificate was renewed", "ends", renewed.Format("2006-01-02"))
	}
}

// checkClock warns, once per stretch, when this machine's clock and the control plane's differ by more than
// maxSkew.
func (r *Reporter) checkClock(server, now time.Time) {
	if server.IsZero() {
		return
	}
	skew := now.Sub(server)
	if skew < 0 {
		skew = -skew
	}
	switch {
	case skew > maxSkew && !r.skewWarned:
		slog.Warn("control plane: the clock of this machine differs from the control plane's; certificates may look not yet valid or ended",
			"difference", skew.Round(time.Second))
		r.skewWarned = true
	case skew <= maxSkew:
		r.skewWarned = false
	}
}
