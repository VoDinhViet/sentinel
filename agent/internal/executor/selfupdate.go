package executor

import (
	"context"
	"fmt"
	"strings"

	"sentinel/agent/internal/selfupdate"
	"sentinel/pkg/agentapi"
)

// UpdateUnit is the transient unit that puts a new binary in place.
const UpdateUnit = "sentinel-self-update"

// UpdateHelpers are the two commands of the binary that run in that unit.
const (
	InstallCommand  = "update-install"
	RollbackCommand = "update-rollback"
)

// Updater fetches and checks a release (selfupdate.Stager).
type Updater interface {
	Stage(ctx context.Context, version string, log func(string)) (selfupdate.Marker, error)
	Forget()
}

// updateAgent replaces the binary of this agent with the release the control plane names by its version. The
// version is all a task gives: the manifest comes from the address in the config, and has to be signed by
// the key in the config (selfupdate.Stager). The binary is put in place by the binary itself, run in a transient
// unit outside the sandbox of the daemon; the daemon restarts after it has reported that this task ended.
func (e *Executor) updateAgent(ctx context.Context, params map[string]string, log func(string)) agentapi.TaskResult {
	if e.Updater == nil {
		return failed(fmt.Errorf("this agent is set up without self_update"))
	}
	version := params["version"]
	marker, err := e.Updater.Stage(ctx, version, log)
	if err != nil {
		return failed(err)
	}
	if err := e.RunUpdateHelper(ctx, InstallCommand, log); err != nil {
		e.Updater.Forget()
		return failed(fmt.Errorf("the new binary could not be put in place: %w", err))
	}
	return succeeded(fmt.Sprintf("updated from %s to %s. The agent restarts to run it; if the new version does not report to the control plane within 5 minutes, the previous binary is put back (%s.prev).", marker.From, marker.To, e.Executable))
}

// RunUpdateHelper runs one of the helper commands of this binary in a transient unit, with fixed arguments.
func (e *Executor) RunUpdateHelper(ctx context.Context, command string, log func(string)) error {
	run, err := e.lookTool("systemd-run")
	if err != nil {
		return err
	}
	args := []string{"--wait", "--collect", "--pipe", "--quiet", "--unit=" + UpdateUnit, "--setenv=LANG=C",
		"--", e.Executable, "--config", e.ConfigPath, command}
	var tail []string
	err = e.runTool(ctx, run, args, func(text string) {
		log(text)
		if tail = append(tail, text); len(tail) > 5 {
			tail = tail[1:]
		}
	})
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.Join(tail, " | "))
	}
	return nil
}
