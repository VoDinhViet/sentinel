package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"sentinel/pkg/agentapi"
)

// maxCommandOutput is how much of what systemctl prints a task keeps.
const maxCommandOutput = 8 << 10

// systemctlPaths are where systemctl is looked for: fixed places, not the PATH of the daemon.
var systemctlPaths = []string{"/usr/bin/systemctl", "/bin/systemctl", "/usr/sbin/systemctl", "/sbin/systemctl"}

// systemctlPath is the first of systemctlPaths that exists.
func systemctlPath() (string, error) {
	for _, path := range systemctlPaths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", errors.New("this machine has no systemctl")
}

// runSystemctl runs systemctl with exactly these arguments and gives back what it printed. There is no shell
// and nothing from a task but the verb and one unit name, which the table and the allowlist have already
// checked: the unit is after a "--", so it cannot be read as an option whatever it looks like. The child
// gets a small environment of its own, not the secrets the daemon was started with.
func runSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	path, err := systemctlPath()
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // path is one of systemctlPaths; the arguments are built in service() from the table
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	cmd.WaitDelay = 5 * time.Second // after a kill, do not wait for a child that kept the pipe open
	both := &limitedWriter{buffer: &output, room: maxCommandOutput}
	cmd.Stdout, cmd.Stderr = both, both // the same writer: exec then copies both streams from one goroutine
	err = cmd.Run()
	return output.Bytes(), err
}

// limitedWriter keeps the first room bytes written to it and drops the rest, without failing the writer.
type limitedWriter struct {
	buffer *bytes.Buffer
	room   int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if take := min(len(p), w.room-w.buffer.Len()); take > 0 {
		w.buffer.Write(p[:take])
	}
	return len(p), nil
}

func (e *Executor) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	if e.Systemctl != nil {
		return e.Systemctl(ctx, args...)
	}
	return runSystemctl(ctx, args...)
}

// service starts, stops or restarts one unit of the allowlist and says what state it is in afterwards.
func (e *Executor) service(ctx context.Context, action, unit string, log func(string)) agentapi.TaskResult {
	verb := strings.TrimPrefix(action, "service.")
	// A unit that is an alias of the agent's own (Alias= in its unit file) is still the agent: ask systemd
	// which unit the name really is before touching it. A name it cannot answer for is not touched either.
	resolved, err := e.systemctl(ctx, "--no-pager", "show", "--property=Id", "--value", "--", unit)
	if resolvedID := firstLine(resolved); err != nil || resolvedID == "" || resolvedID == agentapi.OwnUnit {
		return refused(fmt.Sprintf("%s cannot be told apart from the agent's own unit, or does not exist here: it is not touched", unit))
	}
	log(fmt.Sprintf("systemctl %s %s", verb, unit))
	output, err := e.systemctl(ctx, "--no-ask-password", "--no-pager", verb, "--", unit)
	if err != nil {
		note := ""
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			note = " (it took too long; systemd may still finish the job: look at the unit)"
		}
		return failed(fmt.Errorf("systemctl %s %s: %w: %s%s", verb, unit, err, firstLine(output), note))
	}
	// is-active exits non-zero for a unit that is not active but still says its state: only an empty answer
	// means systemctl did not work.
	answer, _ := e.systemctl(ctx, "--no-pager", "is-active", "--", unit)
	state := strings.TrimSpace(firstLine(answer))
	log(unit + " is " + state)
	switch state {
	case "":
		return failed(fmt.Errorf("systemctl %s %s went through, but the state of the unit could not be read", verb, unit))
	case "failed":
		return failed(fmt.Errorf("%s is failed after %s", unit, verb))
	}
	return succeeded(fmt.Sprintf("%s: %s done, the unit is now %s", unit, verb, state))
}

// firstLine is the first line of what a command printed, as text.
func firstLine(output []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return strings.TrimSpace(line)
}
