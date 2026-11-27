package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/restic"
)

// maxProbeStderr is how much of the end of a tool's error output a failed probe keeps.
const maxProbeStderr = 2048

// lightProbe is implemented by an engine whose dump tool writes output before it has checked
// the database (mysqldump writes its header first, so "the first byte arrived" proves nothing).
// Its probe runs a dump made so light that it can be run to the end, and succeeds only if the
// tool exits cleanly.
type lightProbe interface {
	LightDump(dump Dump) Dump
}

// Probe checks that a backup of the job could connect: it starts the same dump tool a backup
// uses, with the same credentials, and stops it as soon as the tool starts writing the dump.
// A tool that is not installed, a host that does not answer, a wrong password or a database
// that does not exist all end the tool before it writes anything, and its own message is the
// error. The tool does not inherit the variables in dropEnv (the ones the config refers to, as for
// a backup), so a notifier token is not its to see. Nothing is written anywhere and the database is only read. Engines with a lightProbe
// run a schema-free dump to the end instead.
func Probe(ctx context.Context, job *config.Job, dropEnv []string) error {
	dbEngine, ok := For(job.Type)
	if !ok {
		return fmt.Errorf("%q is not a database job", job.Type)
	}
	dump, err := dbEngine.Dump(job)
	if err != nil {
		return err
	}
	if dump.Cleanup != nil {
		defer dump.Cleanup()
	}
	wholeRun := false
	if light, ok := dbEngine.(lightProbe); ok {
		dump, wholeRun = light.LightDump(dump), true
	}
	tool, err := exec.LookPath(dump.Command[0])
	if err != nil {
		return fmt.Errorf("%s is not installed", dump.Command[0])
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, dump.Command[1:]...)
	cmd.Env = append(restic.InheritedEnv(dropEnv), dump.Env...)
	cmd.WaitDelay = 2 * time.Second // a tool that left a child holding its pipes must not hang the probe
	var stderr tailBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", dump.Command[0], err)
	}
	var wrote bool
	if wholeRun {
		_, copyErr := io.Copy(io.Discard, stdout)
		wrote = copyErr == nil
	} else {
		var first [1]byte
		n, _ := io.ReadFull(stdout, first[:])
		wrote = n == 1
	}
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	cancel() // a dump is never read further than the probe needs
	waitErr := cmd.Wait()
	switch {
	case timedOut:
		return fmt.Errorf("no answer from %s in time", dump.Command[0])
	case wholeRun && waitErr == nil, !wholeRun && wrote:
		return nil
	}
	return fmt.Errorf("%s", probeMessage(dump.Command[0], stderr.String()))
}

// probeMessage is what to tell the user about a tool that failed: its own error lines, without
// the warnings it prints on the way and without repeating the tool's name when it does.
func probeMessage(tool, stderr string) string {
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.Contains(line, "Warning:") {
			lines = append(lines, strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, tool+":")), "error: "))
		}
	}
	if len(lines) == 0 {
		return tool + " stopped without saying why"
	}
	return strings.Join(lines[max(len(lines)-2, 0):], " ")
}

// tailBuffer keeps the last maxProbeStderr bytes written to it.
type tailBuffer struct{ data bytes.Buffer }

func (b *tailBuffer) Write(chunk []byte) (int, error) {
	b.data.Write(chunk)
	if excess := b.data.Len() - maxProbeStderr; excess > 0 {
		b.data.Next(excess)
	}
	return len(chunk), nil
}

func (b *tailBuffer) String() string { return b.data.String() }
