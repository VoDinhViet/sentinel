package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"sentinel/pkg/agentapi"
)

// maxCommandOutput is how much of what systemctl prints a task keeps.
const maxCommandOutput = 8 << 10

// runSystemctl runs systemctl with exactly these arguments and gives back what it printed. There is no shell
// and nothing from a task but the verb and one unit name, which the table and the allowlist have already
// checked: the unit is after a "--", so it cannot be read as an option whatever it looks like.
func runSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return nil, fmt.Errorf("this machine has no systemctl: %w", err)
	}
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // path is what LookPath found; the arguments are built in service() from the table
	cmd.Stdout, cmd.Stderr = &limitedWriter{buffer: &output, room: maxCommandOutput}, &limitedWriter{buffer: &output, room: maxCommandOutput}
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
	log(fmt.Sprintf("systemctl %s %s", verb, unit))
	output, err := e.systemctl(ctx, "--no-ask-password", "--no-pager", verb, "--", unit)
	if err != nil {
		return failed(fmt.Errorf("systemctl %s %s: %w: %s", verb, unit, err, firstLine(output)))
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
