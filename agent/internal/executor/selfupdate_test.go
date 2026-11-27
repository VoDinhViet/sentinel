package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/selfupdate"
	"sentinel/pkg/agentapi"
)

type fakeUpdater struct {
	staged    []string
	forgotten int
	err       error
}

func (f *fakeUpdater) Stage(_ context.Context, version string, log func(string)) (selfupdate.Marker, error) {
	f.staged = append(f.staged, version)
	log("staged")
	return selfupdate.Marker{From: "1.0.0", To: version}, f.err
}

func (f *fakeUpdater) Forget() { f.forgotten++ }

func updateExecutor(t *testing.T, updater Updater, fake *toolLog) *Executor {
	t.Helper()
	allow := config.Allow{SelfUpdate: config.SelfUpdate{PublicKey: "k", ManifestURL: "https://releases.example.com/{version}.json"}}
	e := loose(&fakeBackend{}, allow)
	e.Updater, e.Executable, e.ConfigPath = updater, "/usr/local/bin/sentinel", "/etc/sentinel/config.yaml"
	e.LookTool, e.RunTool = fake.look, fake.run
	return e
}

func TestAgentUpdateStagesThenInstallsWithFixedArguments(t *testing.T) {
	fake, updater := &toolLog{}, &fakeUpdater{}
	result, _ := run(updateExecutor(t, updater, fake), task(agentapi.ActionAgentUpdate, map[string]string{"version": "1.2.0"}))
	if result.State != agentapi.TaskSucceeded || !strings.Contains(result.Output, "1.0.0 to 1.2.0") {
		t.Fatalf("result = %+v", result)
	}
	if len(updater.staged) != 1 || updater.staged[0] != "1.2.0" {
		t.Errorf("staged %v", updater.staged)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("calls = %v", fake.calls)
	}
	call := strings.Join(fake.calls[0], " ")
	want := "/usr/bin/systemd-run --wait --collect --pipe --quiet --unit=sentinel-self-update --setenv=LANG=C -- /usr/local/bin/sentinel --config /etc/sentinel/config.yaml update-install"
	if call != want {
		t.Errorf("argv = %s\nwant   %s", call, want)
	}
}

func TestAgentUpdateDoesNotInstallWhatWasNotStaged(t *testing.T) {
	fake, updater := &toolLog{}, &fakeUpdater{err: errors.New("the signature is not valid")}
	result, _ := run(updateExecutor(t, updater, fake), task(agentapi.ActionAgentUpdate, map[string]string{"version": "1.2.0"}))
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "signature") || len(fake.calls) != 0 {
		t.Errorf("result = %+v, calls = %v", result, fake.calls)
	}
}

func TestAgentUpdateForgetsTheStagedBinaryWhenTheInstallFails(t *testing.T) {
	fake, updater := &toolLog{err: errors.New("exit status 1"), lines: []string{"the staged file is not the one that was checked"}}, &fakeUpdater{}
	result, _ := run(updateExecutor(t, updater, fake), task(agentapi.ActionAgentUpdate, map[string]string{"version": "1.2.0"}))
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "not the one that was checked") || updater.forgotten != 1 {
		t.Errorf("result = %+v, forgotten = %d", result, updater.forgotten)
	}
}

func TestAgentUpdateFailsWithoutAnUpdaterAndRefusesWhenNotAllowed(t *testing.T) {
	e := updateExecutor(t, nil, &toolLog{})
	e.Updater = nil
	if result, _ := run(e, task(agentapi.ActionAgentUpdate, map[string]string{"version": "1.2.0"})); result.State != agentapi.TaskFailed {
		t.Errorf("result = %+v", result)
	}
	e = loose(&fakeBackend{}, config.Allow{})
	e.Updater = &fakeUpdater{}
	if result, _ := run(e, task(agentapi.ActionAgentUpdate, map[string]string{"version": "1.2.0"})); result.State != agentapi.TaskRefused {
		t.Errorf("result = %+v", result)
	}
}
