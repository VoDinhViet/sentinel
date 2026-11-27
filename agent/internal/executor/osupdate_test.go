package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"sentinel/agent/internal/config"
	"sentinel/pkg/agentapi"
)

// aptSimulation is what `apt-get -s upgrade` prints on Ubuntu, cut down.
const aptSimulation = `Reading package lists...
Building dependency tree...
The following packages have been kept back:
  linux-generic linux-image-generic
The following packages will be upgraded:
  libc6 openssl
2 upgraded, 0 newly installed, 0 to remove and 2 not upgraded.
Inst libc6 [2.35-0ubuntu3.7] (2.35-0ubuntu3.8 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])
Inst openssl [3.0.2-0ubuntu1.14] (3.0.2-0ubuntu1.15 Ubuntu:22.04/jammy-updates [amd64])
Conf libc6 (2.35-0ubuntu3.8 Ubuntu:22.04/jammy-updates [amd64])
`

// toolLog is a fake apt-get and systemd-run: it finds both, records every call and prints what it is told.
type toolLog struct {
	mu      sync.Mutex
	calls   [][]string
	missing string   // the program LookTool does not find
	lines   []string // what the program prints
	err     error    // how it ends
}

func (f *toolLog) look(name string) (string, error) {
	if name == f.missing {
		return "", errors.New("this machine has no " + name)
	}
	return "/usr/bin/" + name, nil
}

func (f *toolLog) run(_ context.Context, path string, args []string, line func(string)) error {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{path}, args...))
	f.mu.Unlock()
	for _, text := range f.lines {
		line(text)
	}
	return f.err
}

func osExecutor(fake *toolLog, mode string) *Executor {
	e := loose(&fakeBackend{}, config.Allow{OSUpdates: mode})
	e.LookTool = fake.look
	e.RunTool = fake.run
	return e
}

func TestCheckUpdatesRunsASimulationAndReportsWhatItWouldUpgrade(t *testing.T) {
	fake := &toolLog{lines: strings.Split(aptSimulation, "\n")}
	result, _ := run(osExecutor(fake, config.OSUpdatesCheck), task(agentapi.ActionOSCheckUpdates, nil))
	if result.State != agentapi.TaskSucceeded {
		t.Fatalf("%+v", result)
	}
	want := [][]string{{"/usr/bin/apt-get", "-s", "-o", "Debug::NoLocking=1", "upgrade"}}
	if !slices.EqualFunc(fake.calls, want, slices.Equal) {
		t.Errorf("apt-get was called as %q, want %q", fake.calls, want)
	}
	for _, text := range []string{
		"2 packages can be upgraded, 1 of them security updates",
		"2 upgraded, 0 newly installed",
		"2 kept back",
		"linux-generic linux-image-generic",
		"libc6 2.35-0ubuntu3.7 -> 2.35-0ubuntu3.8  [security]",
		"openssl 3.0.2-0ubuntu1.14 -> 3.0.2-0ubuntu1.15",
	} {
		if !strings.Contains(result.Output, text) {
			t.Errorf("the report lacks %q:\n%s", text, result.Output)
		}
	}
	if strings.Contains(result.Output, "openssl 3.0.2-0ubuntu1.14 -> 3.0.2-0ubuntu1.15  [security]") {
		t.Error("openssl is not a security update")
	}
}

func TestCheckUpdatesListsAtMostAHundredPackages(t *testing.T) {
	var lines []string
	for i := range 130 {
		lines = append(lines, "Inst pkg"+strings.Repeat("x", i%3)+" [1] (2 Ubuntu:22.04/jammy-updates [amd64])")
	}
	result, _ := run(osExecutor(&toolLog{lines: lines}, config.OSUpdatesCheck), task(agentapi.ActionOSCheckUpdates, nil))
	if !strings.Contains(result.Output, "130 packages can be upgraded") || !strings.Contains(result.Output, "and 30 more") {
		t.Errorf("%s", result.Output)
	}
	if got := strings.Count(result.Output, " -> "); got != maxUpdateLines {
		t.Errorf("%d packages listed, want %d", got, maxUpdateLines)
	}
}

func TestCheckUpdatesSaysWhenARestartIsWaiting(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "reboot-required")
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := rebootRequiredFile
	rebootRequiredFile = flag
	t.Cleanup(func() { rebootRequiredFile = old })
	result, _ := run(osExecutor(&toolLog{}, config.OSUpdatesCheck), task(agentapi.ActionOSCheckUpdates, nil))
	if !strings.Contains(result.Output, "a restart of this machine is required") {
		t.Errorf("%s", result.Output)
	}
}

func TestApplyUpdatesRunsAptInATransientUnitWithFixedArguments(t *testing.T) {
	fake := &toolLog{lines: []string{"Unpacking libc6", "2 upgraded, 0 newly installed, 0 to remove and 0 not upgraded."}}
	result, lines := run(osExecutor(fake, config.OSUpdatesApply), task(agentapi.ActionOSApplyUpdates, nil))
	if result.State != agentapi.TaskSucceeded || !strings.Contains(result.Output, "2 upgraded") {
		t.Fatalf("%+v", result)
	}
	want := [][]string{{"/usr/bin/systemd-run", "--wait", "--pipe", "--collect", "--quiet", "--unit=sentinel-os-update",
		"--setenv=DEBIAN_FRONTEND=noninteractive", "--setenv=LANG=C",
		"--", "/usr/bin/apt-get", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "upgrade"}}
	if !slices.EqualFunc(fake.calls, want, slices.Equal) {
		t.Errorf("systemd-run was called as %q, want %q", fake.calls, want)
	}
	if !slices.Contains(lines, "Unpacking libc6") {
		t.Errorf("the apt output did not reach the task log: %q", lines)
	}
}

func TestApplyUpdatesFailureKeepsTheTailOfTheOutput(t *testing.T) {
	fake := &toolLog{lines: []string{"E: dpkg was interrupted"}, err: errors.New("exit status 100")}
	result, _ := run(osExecutor(fake, config.OSUpdatesApply), task(agentapi.ActionOSApplyUpdates, nil))
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "dpkg was interrupted") || !strings.Contains(result.Output, "dpkg was interrupted") {
		t.Errorf("%+v", result)
	}
}

func TestApplyUpdatesKeepsOnlyTheLastLines(t *testing.T) {
	var lines []string
	for i := range 500 {
		lines = append(lines, "line "+string(rune('a'+i%26))+strings.Repeat("!", i%7))
	}
	lines = append(lines, "THE END")
	result, _ := run(osExecutor(&toolLog{lines: lines}, config.OSUpdatesApply), task(agentapi.ActionOSApplyUpdates, nil))
	if got := strings.Count(result.Output, "\n"); got > tailLines+10 || !strings.Contains(result.Output, "THE END") {
		t.Errorf("%d lines kept:\n%s", got, result.Output)
	}
}

func TestTheOSActionsAreRefusedUnlessTheAllowlistLetsThemRun(t *testing.T) {
	for _, test := range []struct {
		mode, action string
		refused      bool
	}{
		{"", agentapi.ActionOSCheckUpdates, true},
		{config.OSUpdatesOff, agentapi.ActionOSCheckUpdates, true},
		{config.OSUpdatesCheck, agentapi.ActionOSCheckUpdates, false},
		{config.OSUpdatesApply, agentapi.ActionOSCheckUpdates, false},
		{"", agentapi.ActionOSApplyUpdates, true},
		{config.OSUpdatesCheck, agentapi.ActionOSApplyUpdates, true},
		{config.OSUpdatesApply, agentapi.ActionOSApplyUpdates, false},
	} {
		fake := &toolLog{}
		result, _ := run(osExecutor(fake, test.mode), task(test.action, nil))
		if refused := result.State == agentapi.TaskRefused; refused != test.refused {
			t.Errorf("os_updates %q, %s: %+v", test.mode, test.action, result)
		}
		if test.refused && len(fake.calls) != 0 {
			t.Errorf("os_updates %q, %s: a refused task still ran %q", test.mode, test.action, fake.calls)
		}
		if test.refused && !strings.Contains(result.Error, "control_plane.allow.os_updates") {
			t.Errorf("the refusal does not say what to write: %q", result.Error)
		}
	}
}

func TestTheOSActionsTakeNoParameter(t *testing.T) {
	fake := &toolLog{}
	for _, action := range []string{agentapi.ActionOSCheckUpdates, agentapi.ActionOSApplyUpdates} {
		result, _ := run(osExecutor(fake, config.OSUpdatesApply), task(action, map[string]string{"package": "openssl"}))
		if result.State != agentapi.TaskRefused {
			t.Errorf("%s with a package: %+v", action, result)
		}
	}
	if len(fake.calls) != 0 {
		t.Errorf("ran %q", fake.calls)
	}
}

func TestWithoutAptTheOSActionsFailWithAClearMessage(t *testing.T) {
	for _, action := range []string{agentapi.ActionOSCheckUpdates, agentapi.ActionOSApplyUpdates} {
		fake := &toolLog{missing: "apt-get"}
		result, _ := run(osExecutor(fake, config.OSUpdatesApply), task(action, nil))
		if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "no apt-get") || len(fake.calls) != 0 {
			t.Errorf("%s: %+v", action, result)
		}
	}
	fake := &toolLog{missing: "systemd-run"}
	result, _ := run(osExecutor(fake, config.OSUpdatesApply), task(agentapi.ActionOSApplyUpdates, nil))
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "no systemd-run") {
		t.Errorf("%+v", result)
	}
}

// The real runner reads both streams of a program, line by line, and stops when its context ends.
func TestRunToolReadsBothStreamsAndStopsWhenTheContextEnds(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "tool")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho out\necho err >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var lines []string
	err := runTool(context.Background(), script, nil, func(text string) { lines = append(lines, text) })
	slices.Sort(lines)
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !slices.Equal(lines, []string{"err", "out"}) {
		t.Errorf("err %v, lines %q", err, lines)
	}

	hang := filepath.Join(dir, "hang")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nsleep 30 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200e6)
	defer cancel()
	if err := runTool(ctx, hang, nil, func(string) {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a program that outlives its context: %v", err)
	}
}

func TestLookToolOnlySearchesTheFixedDirectories(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "apt-get")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := toolDirs
	t.Cleanup(func() { toolDirs = old })
	toolDirs = []string{filepath.Join(dir, "elsewhere")}
	t.Setenv("PATH", dir)
	if path, err := lookTool("apt-get"); err == nil {
		t.Errorf("found %s through the PATH", path)
	}
	toolDirs = []string{dir}
	if path, err := lookTool("apt-get"); err != nil || path != tool {
		t.Errorf("%q, %v", path, err)
	}
}

func TestParseSimulationIgnoresTextThatLooksLikeAnInstLine(t *testing.T) {
	upgrades, kept, _ := parseSimulation([]string{"Installing Inst libc6", "inst libc6 [1] (2 x)", "Inst broken line"})
	if len(upgrades) != 0 || len(kept) != 0 {
		t.Errorf("%+v %q", upgrades, kept)
	}
}
