package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/executor"
	"sentinel/agent/internal/selfupdate"
	"sentinel/agent/internal/store"
	"sentinel/agent/internal/transport"
	"sentinel/pkg/agentapi"
)

// startReporter makes the daemon report to the control plane and take its tasks, when the config names one,
// until ctx is done or the returned function is called, which waits for both to stop. A control plane that cannot be
// used (no identity yet, a bad address) is logged and the daemon goes on alone: a backup must not wait for
// the thing that watches it. `sentinel doctor` says what is wrong.
func (a *cliApp) startReporter(ctx context.Context, cfg *config.Config, st *store.Store, restart func()) (stop func()) {
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
	runner := executor.New(service, cfg.ControlPlane.Allow)
	runner.UpdateLog = filepath.Join(filepath.Dir(cfg.StorePath), "os-update.log")
	worker := &transport.Worker{Client: client, Runner: runner}
	reportCtx, cancel := context.WithCancel(ctx)
	var running sync.WaitGroup
	if cfg.ControlPlane.Allow.SelfUpdate.Enabled() {
		a.superviseUpdate(reportCtx, cfg, runner, reporter, worker, restart, &running)
	}
	running.Go(func() { reporter.Loop(reportCtx) })
	running.Go(func() { worker.Loop(reportCtx) })
	slog.Info("control plane: reporting", "url", cfg.ControlPlane.URL, "certificate_ends", client.NotAfter().Format("2006-01-02"),
		"tasks_allowed", cfg.ControlPlane.Allow.Actions())
	return func() { cancel(); running.Wait() }
}

// superviseUpdate sets up agent.update: the executor gets what it needs, a finished update restarts the daemon, and a
// new version that has just been put in place is watched. It has TrialWindow to report to the control plane, and
// the previous binary is put back (and the daemon restarted) if it does not, or if it is started too often.
func (a *cliApp) superviseUpdate(ctx context.Context, cfg *config.Config, runner *executor.Executor, reporter *transport.Reporter,
	worker *transport.Worker, restart func(), running *sync.WaitGroup) {
	dir := selfupdate.Dir(cfg.StorePath)
	self := cfg.ControlPlane.Allow.SelfUpdate
	exe, err := os.Executable() // before any update: after one, this path reads "(deleted)"
	if err != nil {
		slog.Error("self-update: cannot find this binary; agent.update is off", "err", err)
		return
	}
	configPath, err := filepath.Abs(a.configPath)
	if err != nil {
		slog.Error("self-update: cannot find the config; agent.update is off", "err", err)
		return
	}
	runner.Executable, runner.ConfigPath = exe, configPath
	runner.Updater = &selfupdate.Stager{PublicKey: self.PublicKey, URLFor: self.URL, Current: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Dir: dir}
	worker.AfterResult = func(task agentapi.Task, result agentapi.TaskResult) {
		if task.Action == agentapi.ActionAgentUpdate && result.State == agentapi.TaskSucceeded {
			slog.Info("self-update: the new binary is in place; the daemon restarts to run it")
			restart()
		}
	}
	reporter.OnReported = func() { selfupdate.Confirm(dir) }
	verdict, marker, err := selfupdate.BeginTrial(dir, version, time.Now())
	if err != nil {
		slog.Error("self-update: cannot read the note of the update", "err", err)
	}
	goBack := func(reason string) {
		slog.Error("self-update: the new version is taken off", "version", marker.To, "reason", reason)
		helperCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if err := runner.RunUpdateHelper(helperCtx, executor.RollbackCommand, func(line string) { slog.Info("self-update: " + line) }); err != nil {
			slog.Error("self-update: the previous binary could not be put back", "err", err)
			return
		}
		restart()
	}
	switch verdict {
	case selfupdate.GoBack:
		goBack("it was started too often")
	case selfupdate.OnTrial:
		slog.Info("self-update: a new version runs on trial", "version", marker.To, "was", marker.From, "window", selfupdate.TrialWindow)
		running.Go(func() {
			timer := time.NewTimer(selfupdate.TrialWindow)
			defer timer.Stop()
			select {
			case <-ctx.Done():
			case <-timer.C:
				if selfupdate.OnTrialNow(dir) {
					goBack("it did not report to the control plane in time")
				}
			}
		})
	}
}
