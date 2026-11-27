package main

import (
	"context"
	"log/slog"
	"time"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/store"
	"sentinel/agent/internal/transport"
	"sentinel/pkg/agentapi"
)

// startReporter makes the daemon report to the control plane, when the config names one, until ctx is done
// or the returned function is called, which waits for the reporter to stop. A control plane that cannot be
// used (no identity yet, a bad address) is logged and the daemon goes on alone: a backup must not wait for
// the thing that watches it. `sentinel doctor` says what is wrong.
func (a *cliApp) startReporter(ctx context.Context, cfg *config.Config, st *store.Store) (stop func()) {
	if !cfg.ControlPlane.Enabled() {
		return func() {}
	}
	client, err := transport.NewClient(cfg.ControlPlaneDir(), cfg.ControlPlane.URL)
	if err != nil {
		slog.Error("control plane: this agent does not report; it goes on alone (run `sentinel doctor`)", "url", cfg.ControlPlane.URL, "err", err)
		return func() {}
	}
	service := app.New(cfg, st, a.configPath)
	reporter := &transport.Reporter{
		Client: client,
		Build: func(ctx context.Context) (agentapi.Report, error) {
			return service.ControlPlaneReport(ctx, version, time.Now())
		},
	}
	reportCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		reporter.Loop(reportCtx)
	}()
	slog.Info("control plane: reporting", "url", cfg.ControlPlane.URL, "certificate_ends", client.NotAfter().Format("2006-01-02"))
	return func() { cancel(); <-done }
}
