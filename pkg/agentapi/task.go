package agentapi

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"sentinel/pkg/redact"
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
	ActionBackupStart      = "backup.start"
	ActionBackupStop       = "backup.stop"
	ActionBackupSnapshots  = "backup.snapshots"
	ActionDoctorRun        = "doctor.run"
	ActionServiceStart     = "service.start"
	ActionServiceStop      = "service.stop"
	ActionServiceRestart   = "service.restart"
	ActionContainerRestart = "container.restart"
)

// OwnUnit is the unit of the agent itself. No task starts, stops or restarts it: the task would end the
// process that has to report how it went, and a stop could not be undone from here.
const OwnUnit = "sentinel.service"

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
	// The unit and the container are names the agent looks up in its own allowlist; nothing else is taken.
	ActionServiceStart:     {required: []string{"unit"}, timeout: time.Minute},
	ActionServiceStop:      {required: []string{"unit"}, timeout: time.Minute},
	ActionServiceRestart:   {required: []string{"unit"}, timeout: time.Minute},
	ActionContainerRestart: {required: []string{"container"}, optional: []string{"timeout_s"}, timeout: 2 * time.Minute},
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

// The shape of the names a task may carry. They end up as one argument of systemctl (after "--") or as one
// segment of a path of the Docker API, so they are held to the characters those names really use.
var (
	unitName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]*\.(service|timer)$`)
	containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// MaxRestartTimeout is the most seconds a container is given to stop before it is killed.
const MaxRestartTimeout = 120

// ValidUnit says name is a systemd service or timer a task may name: a plain unit name, and not the agent's own.
func ValidUnit(name string) error {
	switch {
	case name == OwnUnit:
		return fmt.Errorf("%s is the agent itself: no task touches it", OwnUnit)
	case !unitName.MatchString(name):
		return fmt.Errorf("%q is not a unit name (letters, digits and : _ . @ - ending in .service or .timer)", termtext.Shorten(termtext.Line(name), 64))
	}
	return nil
}

// ValidContainer says name is a Docker container name or id.
func ValidContainer(name string) error {
	if !containerName.MatchString(name) {
		return fmt.Errorf("%q is not a container name (letters, digits and _ . -)", termtext.Shorten(termtext.Line(name), 64))
	}
	return nil
}

// validTimeout accepts whole seconds from 1 to MaxRestartTimeout.
func validTimeout(value string) error {
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 1 || seconds > MaxRestartTimeout || strconv.Itoa(seconds) != value {
		return fmt.Errorf("timeout_s is whole seconds from 1 to %d", MaxRestartTimeout)
	}
	return nil
}

func validValue(name, value string) error {
	if err := validText(name, value); err != nil {
		return err
	}
	switch name {
	case "unit":
		return ValidUnit(value)
	case "container":
		return ValidContainer(value)
	case "timeout_s":
		return validTimeout(value)
	}
	return nil
}

func validText(name, value string) error {
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
	r.Output = termtext.Shorten(termtext.Clean(redact.Text(r.Output)), MaxTaskOutput)
	r.Error = termtext.Shorten(termtext.Line(redact.Text(r.Error)), MaxTaskError)
	return r
}

// TaskLog is a piece of the log of a task that is running.
type TaskLog struct {
	Text string `json:"text"`
}

// CleanText returns text with credentials masked, stripped of escape sequences and control characters, and cut
// to limit characters. An agent masks its own text first; this is the second line, for an agent that did not.
func CleanText(text string, limit int) string {
	return termtext.Shorten(termtext.Clean(redact.Text(text)), limit)
}
