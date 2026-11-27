package agentapi

import (
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"sentinel/pkg/termtext"
)

// Routes of the tasks of an agent. The task id is in the path.
const (
	RouteTasks      = "/v1/agent/tasks"
	RouteTaskResult = "/v1/agent/tasks/{id}/result"
	RouteTaskLog    = "/v1/agent/tasks/{id}/log"
)

// The actions an agent can be asked to do. This table is all there is: no action runs a shell, and none
// takes a path, a package name or a command. A parameter is a name from the agent's own config, and the
// agent checks it there again, after checking that its local allowlist lets the action run.
const (
	ActionBackupStart     = "backup.start"
	ActionBackupStop      = "backup.stop"
	ActionBackupSnapshots = "backup.snapshots"
	ActionDoctorRun       = "doctor.run"
)

// The states of a task. A task is queued, then sent to the agent that asked for it, then ends in one of
// succeeded, failed (it ran and did not work), refused (the agent would not run it), canceled (taken back
// before it was sent) or expired (nobody took it, or no result came in time).
const (
	TaskQueued    = "queued"
	TaskSent      = "sent"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"
	TaskRefused   = "refused"
	TaskCanceled  = "canceled"
	TaskExpired   = "expired"
)

// Finished says a task in this state has ended and will not change.
func Finished(state string) bool { return state != TaskQueued && state != TaskSent }

// The limits on what an agent sends back about a task: it comes from another machine, so the control
// plane keeps it to these however much the agent sent.
const (
	MaxTaskOutput   = 64 << 10 // characters of the result of a task
	MaxTaskError    = 2 << 10
	MaxTaskLogChunk = 16 << 10  // characters of one piece of the log of a running task
	MaxTaskLog      = 256 << 10 // characters of the whole log of a task
	// maxParamLen is the longest value a parameter may have, in characters.
	maxParamLen = 128
	// ResultGrace is the time an agent has beyond the timeout of its action to send the result.
	ResultGrace = 30 * time.Second
)

// actionSpec is what the table says of an action.
type actionSpec struct {
	required, optional []string
	timeout            time.Duration
}

var actionTable = map[string]actionSpec{
	ActionBackupStart:     {required: []string{"job"}, optional: []string{"repository"}, timeout: 2 * time.Minute},
	ActionBackupStop:      {required: []string{"job"}, timeout: time.Minute},
	ActionBackupSnapshots: {required: []string{"job"}, optional: []string{"repository"}, timeout: 3 * time.Minute},
	ActionDoctorRun:       {timeout: 3 * time.Minute},
}

// Actions are the names of the actions, in order.
func Actions() []string {
	names := make([]string, 0, len(actionTable))
	for name := range actionTable {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Params are the names of the parameters of an action, the ones it needs first.
func Params(action string) (required, optional []string) {
	spec := actionTable[action]
	return slices.Clone(spec.required), slices.Clone(spec.optional)
}

// Timeout is how long an agent may take over an action.
func Timeout(action string) time.Duration { return actionTable[action].timeout }

// ValidateTask checks an action and its parameters against the table: the action is in it, every needed
// parameter is there, no other is, and each value is one short line of text. Both the control plane (before
// it queues a task) and the agent (before it runs one) call it, so a task that one refuses the other would too.
func ValidateTask(action string, params map[string]string) error {
	spec, known := actionTable[action]
	if !known {
		return fmt.Errorf("unknown action %q", termtext.Shorten(termtext.Line(action), 64))
	}
	for name, value := range params {
		if !slices.Contains(spec.required, name) && !slices.Contains(spec.optional, name) {
			return fmt.Errorf("%s does not take the parameter %q", action, termtext.Shorten(termtext.Line(name), 64))
		}
		if err := validValue(name, value); err != nil {
			return err
		}
	}
	for _, name := range spec.required {
		if params[name] == "" {
			return fmt.Errorf("%s needs the parameter %s", action, name)
		}
	}
	return nil
}

func validValue(name, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("the parameter %s is empty", name)
	case utf8.RuneCountInString(value) > maxParamLen:
		return fmt.Errorf("the parameter %s is longer than %d characters", name, maxParamLen)
	case termtext.Line(value) != value:
		return fmt.Errorf("the parameter %s has a control character or a line break", name)
	}
	return nil
}

// Task is what an agent is given to do.
type Task struct {
	ID     int64             `json:"id"`
	Action string            `json:"action"`
	Params map[string]string `json:"params"`
	// ResultBy is when the control plane gives up waiting for the result.
	ResultBy time.Time `json:"result_by"`
}

// TaskResult is what an agent says of a task it has finished (or refused).
type TaskResult struct {
	State      string    `json:"state"` // succeeded, failed or refused
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Output     string    `json:"output"` // what the task produced; may have several lines
	Error      string    `json:"error"`  // one line, when it did not succeed
}

// ValidState says an agent may report the state: it is the one thing it cannot choose freely.
func ValidState(state string) bool {
	return state == TaskSucceeded || state == TaskFailed || state == TaskRefused
}

// Clean returns the result with its texts stripped of escape sequences and control characters and cut to
// the limits. Whoever receives a result from an agent calls it before keeping or showing it.
func (r TaskResult) Clean() TaskResult {
	r.Output = termtext.Shorten(termtext.Clean(r.Output), MaxTaskOutput)
	r.Error = termtext.Shorten(termtext.Line(r.Error), MaxTaskError)
	return r
}

// TaskLog is a piece of the log of a task that is running.
type TaskLog struct {
	Text string `json:"text"`
}

// CleanText returns text stripped of escape sequences and control characters, cut to limit characters.
func CleanText(text string, limit int) string { return termtext.Shorten(termtext.Clean(text), limit) }
