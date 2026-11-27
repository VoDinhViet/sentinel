package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	unit    []string // what the unit of an upgrade writes to its log file
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
	for _, arg := range args { // systemd-run: the unit writes where it was told to
		if file, found := strings.CutPrefix(arg, "--property=StandardOutput=append:"); found && len(f.unit) > 0 {
			if err := os.WriteFile(file, []byte(strings.Join(f.unit, "\n")+"\n"), 0o600); err != nil {
				return err
			}
		}
	}
	for _, text := range f.lines {
		line(text)
	}
	return f.err
}

func osExecutor(t *testing.T, fake *toolLog, mode string) *Executor {
	t.Helper()
	e := loose(&fakeBackend{}, config.Allow{OSUpdates: mode})
	e.UpdateLog = filepath.Join(t.TempDir(), "os-update.log")
	e.LookTool = fake.look
	e.RunTool = fake.run
	return e
}

func TestCheckUpdatesRunsASimulationAndReportsWhatItWouldUpgrade(t *testing.T) {
	fake := &toolLog{lines: strings.Split(aptSimulation, "\n")}
	result, _ := run(osExecutor(t, fake, config.OSUpdatesCheck), task(agentapi.ActionOSCheckUpdates, nil))
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
	result, _ := run(osExecutor(t, &toolLog{lines: lines}, config.OSUpdatesCheck), task(agentapi.ActionOSCheckUpdates, nil))
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
	result, _ := run(osExecutor(t, &toolLog{}, config.OSUpdatesCheck), task(agentapi.ActionOSCheckUpdates, nil))
	if !strings.Contains(result.Output, "a restart of this machine is required") {
		t.Errorf("%s", result.Output)
	}
}

func TestApplyUpdatesRunsAptInATransientUnitWritingToAFileOfTheAgent(t *testing.T) {
	fake := &toolLog{unit: []string{"Unpacking libc6", "2 upgraded, 0 newly installed, 0 to remove and 0 not upgraded."}}
	e := osExecutor(t, fake, config.OSUpdatesApply)
	result, lines := run(e, task(agentapi.ActionOSApplyUpdates, nil))
	if result.State != agentapi.TaskSucceeded || !strings.Contains(result.Output, "2 upgraded") {
		t.Fatalf("%+v", result)
	}
	output := "append:" + e.UpdateLog
	want := [][]string{{"/usr/bin/systemd-run", "--wait", "--collect", "--unit=sentinel-os-update",
		"--property=StandardOutput=" + output, "--property=StandardError=" + output,
		"--setenv=DEBIAN_FRONTEND=noninteractive", "--setenv=LANG=C",
		"--", "/usr/bin/apt-get", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "upgrade"}}
	if !slices.EqualFunc(fake.calls, want, slices.Equal) {
		t.Errorf("systemd-run was called as %q, want %q", fake.calls, want)
	}
	if !slices.Contains(lines, "Unpacking libc6") {
		t.Errorf("the output of the unit did not reach the task log: %q", lines)
	}
	if info, err := os.Stat(e.UpdateLog); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the log of the upgrade: %v, %v", info, err)
	}
}

func TestApplyUpdatesNeedsAPlaceForTheLog(t *testing.T) {
	e := osExecutor(t, &toolLog{}, config.OSUpdatesApply)
	e.UpdateLog = ""
	if result, _ := run(e, task(agentapi.ActionOSApplyUpdates, nil)); result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "no place") {
		t.Errorf("%+v", result)
	}
	e.UpdateLog = filepath.Join(t.TempDir(), "log")
	if err := os.Symlink("/etc/passwd", e.UpdateLog); err != nil {
		t.Fatal(err)
	}
	if result, _ := run(e, task(agentapi.ActionOSApplyUpdates, nil)); result.State != agentapi.TaskFailed {
		t.Errorf("a symlink was followed: %+v", result)
	}
}

func TestApplyUpdatesFailureKeepsTheTailOfTheOutput(t *testing.T) {
	fake := &toolLog{unit: []string{"E: dpkg was interrupted"}, err: errors.New("exit status 100")}
	result, _ := run(osExecutor(t, fake, config.OSUpdatesApply), task(agentapi.ActionOSApplyUpdates, nil))
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
	result, _ := run(osExecutor(t, &toolLog{unit: lines}, config.OSUpdatesApply), task(agentapi.ActionOSApplyUpdates, nil))
	if got := strings.Count(result.Output, "\n"); got > tailLines+10 || !strings.Contains(result.Output, "THE END") {
		t.Errorf("%d lines kept:\n%s", got, result.Output)
	}
}

// When the time of the task is up the agent stops waiting, and the unit must not notice: nothing it writes to is
// closed, and the message says it goes on.
func TestApplyUpdatesThatRunOutOfTimeLeaveTheUnitAlone(t *testing.T) {
	fake := &toolLog{unit: []string{"Setting up libc6"}, err: context.DeadlineExceeded}
	e := osExecutor(t, fake, config.OSUpdatesApply)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.RunTool = func(ctx context.Context, path string, args []string, line func(string)) error {
		_ = fake.run(ctx, path, args, line)
		return context.Canceled
	}
	result := e.applyUpdates(ctx, func(string) {})
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "the upgrade goes on in the unit sentinel-os-update") || !strings.Contains(result.Output, "Setting up libc6") {
		t.Errorf("%+v", result)
	}
	if _, err := os.Stat(e.UpdateLog); err != nil {
		t.Errorf("the log was removed: %v", err)
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
		result, _ := run(osExecutor(t, fake, test.mode), task(test.action, nil))
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
		result, _ := run(osExecutor(t, fake, config.OSUpdatesApply), task(action, map[string]string{"package": "openssl"}))
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
		result, _ := run(osExecutor(t, fake, config.OSUpdatesApply), task(action, nil))
		if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "no apt-get") || len(fake.calls) != 0 {
			t.Errorf("%s: %+v", action, result)
		}
	}
	fake := &toolLog{missing: "systemd-run"}
	result, _ := run(osExecutor(t, fake, config.OSUpdatesApply), task(agentapi.ActionOSApplyUpdates, nil))
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
	pidFile := filepath.Join(dir, "pid")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nsleep 30 &\necho $! > "+pidFile+"\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if pid, err := os.ReadFile(pidFile); err == nil {
			_ = exec.Command("kill", strings.TrimSpace(string(pid))).Run()
		}
	})
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

// A program that prints a long line with no line break must not be stopped by it: the pipe is read to the end,
// the line is cut, and what comes after it is still seen. Otherwise the program blocks on a full pipe, with
// dpkg's lock in its hand.
func TestRunToolReadsToTheEndOfAVeryLongLine(t *testing.T) {
	script := filepath.Join(t.TempDir(), "tool")
	body := "#!/bin/sh\nhead -c 1000000 /dev/zero | tr '\\0' x\nprintf '\\rprogress 50%%\\rprogress 100%%\\n'\necho 'N upgraded'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	var lines []string
	if err := runTool(t.Context(), script, nil, func(text string) { lines = append(lines, text) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 || len(lines[0]) != maxLineLen || lines[1] != "progress 50%" || lines[2] != "progress 100%" || lines[3] != "N upgraded" {
		t.Errorf("%d lines: first is %d long, then %q", len(lines), len(lines[0]), lines[1:])
	}
}

func TestACleanExitIsNotAFailureBecauseTheContextEndedAfterwards(t *testing.T) {
	script := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runTool(t.Context(), script, nil, func(string) {}); err != nil {
		t.Errorf("%v", err)
	}
}
