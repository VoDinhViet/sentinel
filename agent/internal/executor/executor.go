// Package executor runs the tasks the control plane gives this agent. It is the last word: a task is
// checked against the fixed table of actions (the same check the control plane made), then against the
// local allowlist of the config (control_plane.allow, everything off by default), then against a rate
// limit, and only then does something. What it does is a call of the same service the CLI and the manager
// use, with parameters that are names from this machine's own config: no task runs a shell or takes a path.
package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/doctor"
	"sentinel/agent/internal/restic"
	"sentinel/pkg/agentapi"
	"sentinel/pkg/ratelimit"
)

// maxSnapshots is how many snapshots a snapshot listing shows: the newest. maxLogLines is how many lines of
// progress one task sends: more are dropped, so a task cannot flood the control plane.
const (
	maxSnapshots = 50
	maxLogLines  = 200
)

// Backend is what a task can do, all of it a method of app.Service.
type Backend interface {
	StartBackup(jobName, repo string) (pid int, logPath string, err error)
	RequestStop(jobName string) error
	ListSnapshots(ctx context.Context, jobName, repo string) ([]restic.Snapshot, error)
	JobRepositories() []app.JobRepository
	Doctor(ctx context.Context) []doctor.Finding
}

// Executor runs tasks.
type Executor struct {
	Backend Backend
	Allow   config.Allow
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Systemctl runs systemctl with the given arguments; nil runs the real one. A test replaces it.
	Systemctl func(ctx context.Context, args ...string) ([]byte, error)
	// LookTool finds apt-get and systemd-run, and RunTool runs a program line by line; nil means the real ones.
	LookTool func(name string) (string, error)
	// UpdateLog is the file the unit of an upgrade writes its output to, in the directory of the store.
	UpdateLog string
	// Updater, Executable and ConfigPath are for agent.update: the stager (nil without allow.self_update), the path
	// of the running binary, and the config the helper in the transient unit reads.
	Updater    Updater
	Executable string
	ConfigPath string
	RunTool    func(ctx context.Context, path string, args []string, line func(string)) error

	limit *ratelimit.Limiter
}

// New returns an executor for the backend and the allowlist.
func New(backend Backend, allow config.Allow) *Executor {
	// A few of an action at once, then one every five seconds: a loop on the control plane's side (or
	// somebody else's) cannot make this machine start backups all day.
	return &Executor{Backend: backend, Allow: allow, limit: ratelimit.New(0.2, 3)}
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Run does the task and says how it ended. It never panics on a bad task and never returns without a state:
// a task that is refused, or that cannot be done, is a result like any other. Every task is written to the
// log of this machine, which the control plane cannot reach.
func (e *Executor) Run(ctx context.Context, task agentapi.Task, log func(line string)) agentapi.TaskResult {
	started := e.now()
	slog.Info("task received", "task", task.ID, "action", task.Action, "params", task.Params)
	sent := 0
	result := e.run(ctx, task, func(line string) { // what restic or a backend printed can quote a key
		switch sent++; {
		case sent <= maxLogLines:
			log(restic.Redact(line))
		case sent == maxLogLines+1:
			log("(more lines than this log keeps: the result has the end of the output)")
		}
	})
	result.StartedAt, result.FinishedAt = started, e.now()
	result.Output, result.Error = restic.Redact(result.Output), restic.Redact(result.Error)
	slog.Info("task finished", "task", task.ID, "action", task.Action, "state", result.State, "error", result.Error)
	return result.Clean()
}

// run is Run without its bookkeeping. A panic in the code a task calls ends that task as failed: the daemon, with
// its scheduled backups, must not go down with it.
func (e *Executor) run(ctx context.Context, task agentapi.Task, log func(line string)) (result agentapi.TaskResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("task panicked", "task", task.ID, "action", task.Action, "panic", recovered)
			result = agentapi.TaskResult{State: agentapi.TaskFailed, Error: "the task stopped on an internal error; see the log of this server"}
		}
	}()
	if err := agentapi.ValidateTask(task.Action, task.Params); err != nil {
		return refused("the task is not one this agent knows: " + err.Error())
	}
	if !e.Allow.Permits(task.Action, task.Params) {
		return refused(fmt.Sprintf("%s is not allowed on this server: set %s in its config and restart sentinel", task.Action, e.Allow.Hint(task.Action, task.Params)))
	}
	if !e.limit.Allow(task.Action) {
		return refused("too many " + task.Action + " tasks in a row: wait a few seconds")
	}
	ctx, cancel := context.WithTimeout(ctx, agentapi.Timeout(task.Action))
	defer cancel()
	switch task.Action {
	case agentapi.ActionBackupStart:
		return e.startBackup(task.Params, log)
	case agentapi.ActionBackupStop:
		return e.stopBackup(task.Params["job"], log)
	case agentapi.ActionBackupSnapshots:
		return e.snapshots(ctx, task.Params, log)
	case agentapi.ActionServiceStart, agentapi.ActionServiceStop, agentapi.ActionServiceRestart:
		return e.service(ctx, task.Action, task.Params["unit"], log)
	case agentapi.ActionContainerRestart:
		return e.restartContainer(ctx, task.Params, log)
	case agentapi.ActionOSCheckUpdates:
		return e.checkUpdates(ctx, log)
	case agentapi.ActionOSApplyUpdates:
		return e.applyUpdates(ctx, log)
	case agentapi.ActionAgentUpdate:
		return e.updateAgent(ctx, task.Params, log)
	default: // agentapi.ActionDoctorRun: ValidateTask let nothing else through
		return e.doctor(ctx, log)
	}
}

func refused(why string) agentapi.TaskResult {
	return agentapi.TaskResult{State: agentapi.TaskRefused, Error: why}
}

func failed(err error) agentapi.TaskResult {
	return agentapi.TaskResult{State: agentapi.TaskFailed, Error: oneLine(err.Error())}
}

func succeeded(output string) agentapi.TaskResult {
	return agentapi.TaskResult{State: agentapi.TaskSucceeded, Output: output}
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

func (e *Executor) startBackup(params map[string]string, log func(string)) agentapi.TaskResult {
	job, repo := params["job"], params["repository"]
	log("starting the backup of job " + job)
	pid, _, err := e.Backend.StartBackup(job, repo)
	switch {
	case errors.Is(err, app.ErrNotFound):
		return failed(fmt.Errorf("this server has no such job or repository in its config: %w", err))
	case errors.Is(err, app.ErrRunning):
		return failed(fmt.Errorf("the backup of %s is already running: %w", job, err))
	case err != nil:
		return failed(err)
	}
	log(fmt.Sprintf("the backup is running (pid %d)", pid))
	return succeeded(fmt.Sprintf("the backup of job %s has started (pid %d); it goes on by itself, and its result is in the next report", job, pid))
}

func (e *Executor) stopBackup(job string, log func(string)) agentapi.TaskResult {
	log("asking the backup of job " + job + " to stop")
	if err := e.Backend.RequestStop(job); err != nil {
		return failed(err)
	}
	return succeeded("the backup of job " + job + " was asked to stop; it ends at its next safe point")
}

func (e *Executor) snapshots(ctx context.Context, params map[string]string, log func(string)) agentapi.TaskResult {
	job, repo := params["job"], params["repository"]
	repo, err := e.repositoryOf(job, repo)
	if err != nil {
		return failed(err)
	}
	log(fmt.Sprintf("listing the snapshots of job %s in %s", job, repo))
	found, err := e.Backend.ListSnapshots(ctx, job, repo)
	switch {
	case errors.Is(err, app.ErrNotFound):
		return failed(fmt.Errorf("this server has no such job or repository in its config: %w", err))
	case err != nil:
		return failed(err)
	}
	if len(found) == 0 {
		return succeeded("no snapshots")
	}
	var lines []string
	for _, snap := range found[:min(len(found), maxSnapshots)] {
		lines = append(lines, fmt.Sprintf("%s  %s  %s  %s", snap.ShortID, snap.Time.Local().Format("2006-01-02 15:04"), snap.Hostname, strings.Join(snap.Paths, ",")))
	}
	if len(found) > maxSnapshots {
		lines = append(lines, fmt.Sprintf("… and %d older", len(found)-maxSnapshots))
	}
	return succeeded(strings.Join(lines, "\n"))
}

// repositoryOf is the repository to list: the one asked for, or the only one the job has.
func (e *Executor) repositoryOf(job, repo string) (string, error) {
	if repo != "" {
		return repo, nil
	}
	var repos []string
	for _, backup := range e.Backend.JobRepositories() {
		if backup.Job == job {
			repos = append(repos, backup.Repository)
		}
	}
	switch len(repos) {
	case 0:
		return "", fmt.Errorf("this server has no job %q in its config", job)
	case 1:
		return repos[0], nil
	}
	return "", fmt.Errorf("job %s goes to %d repositories (%s): say which with repository=…", job, len(repos), strings.Join(repos, ", "))
}

func (e *Executor) doctor(ctx context.Context, log func(string)) agentapi.TaskResult {
	log("running the checks of sentinel doctor")
	var lines []string
	var failures int
	for _, found := range e.Backend.Doctor(ctx) {
		level, message := "ok", found.Info
		switch {
		case found.Err != nil && found.Warn:
			level, message = "warning", found.Err.Error()
		case found.Err != nil:
			level, message = "failed", found.Err.Error()
			failures++
		}
		line := fmt.Sprintf("%-8s %s: %s", level, found.Name, oneLine(message))
		log(line)
		lines = append(lines, line)
	}
	result := succeeded(strings.Join(lines, "\n"))
	if failures > 0 {
		result.State, result.Error = agentapi.TaskFailed, fmt.Sprintf("%d of %d checks failed", failures, len(lines))
	}
	return result
}
