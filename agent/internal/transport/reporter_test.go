package transport

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"sentinel/pkg/agentapi"
)

// fakeSender is a control plane that can be told how to answer.
type fakeSender struct {
	mu         sync.Mutex
	reports    []agentapi.Report
	renewed    int
	notAfter   time.Time
	reportErr  error
	renewErr   error
	everySecs  int
	serverTime func() time.Time
	pings      int
	reloads    int
}

func (f *fakeSender) Reload() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	return nil
}

func (f *fakeSender) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	return f.reportErr
}

func (f *fakeSender) Report(_ context.Context, report agentapi.Report) (agentapi.ReportResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reportErr != nil {
		return agentapi.ReportResponse{}, f.reportErr
	}
	f.reports = append(f.reports, report)
	answer := agentapi.ReportResponse{ReportEverySeconds: f.everySecs, ServerTime: time.Now()}
	if f.serverTime != nil {
		answer.ServerTime = f.serverTime()
	}
	return answer, nil
}

func (f *fakeSender) Renew(context.Context) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewErr != nil {
		return time.Time{}, f.renewErr
	}
	f.renewed++
	f.notAfter = time.Now().Add(90 * 24 * time.Hour)
	return f.notAfter, nil
}

func (f *fakeSender) NotAfter() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notAfter
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reports)
}

func newReporter(sender *fakeSender) *Reporter {
	return &Reporter{Client: sender, Build: func(context.Context) (agentapi.Report, error) { return agentapi.Report{Version: "1"}, nil }}
}

func unauthorized() error { return &statusError{status: 401, message: "no"} }

func TestLoopReportsAtThePaceTheControlPlaneAsksFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &fakeSender{everySecs: 30, notAfter: time.Now().Add(80 * 24 * time.Hour)}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { newReporter(sender).Loop(ctx); close(done) }()

		time.Sleep(time.Second)
		if sender.count() != 1 {
			t.Fatalf("the first report is sent at once: %d", sender.count())
		}
		time.Sleep(10 * time.Minute)
		// every 30s give or take 10%: between 10*60/33 = 18 and 10*60/27 = 22 more reports
		if got := sender.count(); got < 19 || got > 24 {
			t.Errorf("reports in 10 minutes at a 30s pace = %d", got)
		}
		cancel()
		<-done
		if sender.renewed != 0 {
			t.Error("a certificate with 80 days left was renewed")
		}
	})
}

func TestACertificateNearItsEndIsRenewedBeforeTheReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &fakeSender{notAfter: time.Now().Add(20 * 24 * time.Hour)}
		reporter := newReporter(sender)
		reporter.Round(t.Context())
		if sender.renewed != 1 || sender.count() != 1 {
			t.Fatalf("renewed %d, reports %d", sender.renewed, sender.count())
		}
		reporter.Round(t.Context())
		if sender.renewed != 1 {
			t.Error("renewed again with 90 days left")
		}
	})
}

func TestARenewalThatFailsIsTriedAgainLaterNotEveryRound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &fakeSender{notAfter: time.Now().Add(20 * 24 * time.Hour), renewErr: errors.New("down")}
		reporter := newReporter(sender)
		slog.SetDefault(slog.New(slog.DiscardHandler))
		reporter.Round(t.Context())
		time.Sleep(time.Minute)
		reporter.Round(t.Context())
		if sender.renewed != 0 || sender.count() != 2 {
			t.Fatalf("the reports go on while renewal fails: renewed %d, reports %d", sender.renewed, sender.count())
		}
		sender.renewErr = nil
		time.Sleep(61 * time.Minute)
		reporter.Round(t.Context())
		if sender.renewed != 1 {
			t.Errorf("renewed %d times after the retry delay", sender.renewed)
		}
	})
}

func TestARefusedCertificateStopsTheReportsForAnHour(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &fakeSender{notAfter: time.Now().Add(80 * 24 * time.Hour), reportErr: unauthorized()}
		reporter := newReporter(sender)
		reporter.Round(t.Context())
		calls := 0
		reporter.Build = func(context.Context) (agentapi.Report, error) { calls++; return agentapi.Report{}, nil }
		time.Sleep(30 * time.Minute)
		reporter.Round(t.Context())
		if calls != 0 {
			t.Error("the reporter went on asking while it was refused")
		}
		sender.reportErr = nil
		time.Sleep(31 * time.Minute)
		reporter.Round(t.Context())
		if sender.count() != 1 {
			t.Errorf("after the hour: %d reports, want 1", sender.count())
		}
	})
}

func TestAFailedReportDoesNotStopTheNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &fakeSender{notAfter: time.Now().Add(80 * 24 * time.Hour), reportErr: errors.New("unreachable")}
		reporter := newReporter(sender)
		reporter.Round(t.Context())
		sender.reportErr = nil
		reporter.Round(t.Context())
		if sender.count() != 1 {
			t.Errorf("reports = %d", sender.count())
		}
	})
}

func TestAfterARefusalTheIdentityIsReadAgainBeforeTheNextTry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &fakeSender{notAfter: time.Now().Add(80 * 24 * time.Hour), reportErr: unauthorized()}
		reporter := newReporter(sender)
		reporter.Round(t.Context())
		time.Sleep(30 * time.Minute)
		reporter.Round(t.Context())
		if sender.reloads != 0 {
			t.Error("the identity was read again while the pause lasted")
		}
		sender.reportErr = nil // the operator joined again
		time.Sleep(31 * time.Minute)
		reporter.Round(t.Context())
		if sender.reloads != 1 || sender.count() != 1 {
			t.Errorf("reloads %d, reports %d: want the identity read once, then a report", sender.reloads, sender.count())
		}
	})
}

func TestAReportThatCannotBeBuiltStillShowsTheAgentIsAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &fakeSender{notAfter: time.Now().Add(80 * 24 * time.Hour)}
		reporter := newReporter(sender)
		reporter.Build = func(context.Context) (agentapi.Report, error) {
			return agentapi.Report{}, errors.New("database is locked")
		}
		if !reporter.Round(t.Context()) || sender.pings != 1 || sender.count() != 0 {
			t.Errorf("pings %d, reports %d", sender.pings, sender.count())
		}
	})
}

func TestAFailedReportIsTriedAgainSoonerThanThePace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &fakeSender{notAfter: time.Now().Add(80 * 24 * time.Hour), reportErr: errors.New("timeout")}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { newReporter(sender).Loop(ctx); close(done) }()
		time.Sleep(time.Second)
		sender.mu.Lock()
		sender.reportErr = nil
		sender.mu.Unlock()
		time.Sleep(30 * time.Second) // well before the 60s pace
		if sender.count() == 0 {
			t.Error("the report was not tried again within 30 seconds of the failure")
		}
		cancel()
		<-done
	})
}

func TestAnAnswerWithoutAPaceLeavesThePaceAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &fakeSender{notAfter: time.Now().Add(80 * 24 * time.Hour), everySecs: 0}
		reporter := newReporter(sender)
		reporter.Round(t.Context())
		if reporter.pace() != time.Duration(agentapi.DefaultReportGap)*time.Second {
			t.Errorf("pace = %s after an answer that named none", reporter.pace())
		}
	})
}
