package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/termtext"
	"sentinel/server/internal/ctlapi"
)

// cleanTask strips what an agent wrote in a task of escape sequences and control characters, whatever the
// control plane did: it is shown on a terminal.
func cleanTask(task *ctlapi.Task) {
	task.Agent, task.Action, task.CreatedBy = termtext.Line(task.Agent), termtext.Line(task.Action), termtext.Line(task.CreatedBy)
	task.Output, task.Error = agentapi.CleanText(task.Output, agentapi.MaxTaskOutput), termtext.Line(task.Error)
	params := make(map[string]string, len(task.Params))
	for name, value := range task.Params {
		params[termtext.Line(name)] = termtext.Line(value)
	}
	task.Params = params
}

func cleanLine(line *ctlapi.TaskLine) {
	line.Text = agentapi.CleanText(line.Text, agentapi.MaxTaskLogChunk)
}

// CreateTask queues an action for a server.
func (c *Client) CreateTask(ctx context.Context, agent, action string, params map[string]string) (ctlapi.Task, error) {
	var created ctlapi.Task
	err := c.do(ctx, http.MethodPost, ctlapi.RouteTasks, ctlapi.CreateTaskRequest{Agent: agent, Action: action, Params: params}, &created)
	cleanTask(&created)
	return created, err
}

// Tasks are the newest tasks, of one server or (with "") of all.
func (c *Client) Tasks(ctx context.Context, agent string, limit int) ([]ctlapi.Task, error) {
	var tasks []ctlapi.Task
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	if agent != "" {
		query.Set("agent", agent)
	}
	err := c.do(ctx, http.MethodGet, ctlapi.RouteTasks+"?"+query.Encode(), nil, &tasks)
	for i := range tasks {
		cleanTask(&tasks[i])
	}
	return tasks, err
}

func taskRoute(id int64) string {
	return strings.Replace(ctlapi.RouteTask, "{id}", strconv.FormatInt(id, 10), 1)
}

// Task is one task with its log.
func (c *Client) Task(ctx context.Context, id int64) (ctlapi.TaskDetail, error) {
	var detail ctlapi.TaskDetail
	err := c.do(ctx, http.MethodGet, taskRoute(id), nil, &detail)
	cleanTask(&detail.Task)
	for i := range detail.Log {
		cleanLine(&detail.Log[i])
	}
	return detail, err
}

// CancelTask takes back a task that no server has taken yet.
func (c *Client) CancelTask(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, taskRoute(id), nil, nil)
}

// TaskEvent is one event of the stream of a task: Kind says which of the fields is set.
type TaskEvent struct {
	Kind  string // ctlapi.EventState, EventLog or EventDone
	State string
	Line  ctlapi.TaskLine
	Task  ctlapi.Task
}

// The stream of a task is read with its own client: the one for calls ends a request after 30 seconds, and
// a task may take minutes. A cut connection is picked up again from the last line seen, a few times.
const (
	followRetries = 5
	followPause   = time.Second
	// maxEventLine is the longest line of the stream: the end of a task carries its output, up to 64K
	// characters, which JSON may write with six bytes each.
	maxEventLine = 1 << 20
)

// FollowTask calls onEvent for the events of a task, until it has ended, and returns it as it ended. A
// stream that is cut is resumed from the last line it gave (Last-Event-ID); after a few cuts in a row it
// gives up with the error. If onEvent returns an error, following stops with it.
func (c *Client) FollowTask(ctx context.Context, id int64, onEvent func(TaskEvent) error) (ctlapi.Task, error) {
	lastLine, failures := "", 0
	for {
		before := lastLine
		task, ended, err := c.followOnce(ctx, id, &lastLine, onEvent)
		if lastLine != before { // the stream gave lines before it was cut: the cuts counted are in a row
			failures = 0
		}
		switch {
		case ended:
			return task, nil
		case errors.Is(err, ErrSessionEnded), ctx.Err() != nil, isStopped(err):
			return ctlapi.Task{}, firstError(ctx.Err(), err)
		}
		if failures++; failures > followRetries {
			return ctlapi.Task{}, fmt.Errorf("the stream of task %d was cut too many times: %w", id, firstError(err, io.ErrUnexpectedEOF))
		}
		select {
		case <-ctx.Done():
			return ctlapi.Task{}, ctx.Err()
		case <-time.After(followPause):
		}
	}
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// stopped is what a caller's own error from onEvent is wrapped in, so FollowTask does not retry it.
type stopped struct{ error }

func (e stopped) Unwrap() error { return e.error }

func isStopped(err error) bool { var s stopped; return errors.As(err, &s) }

// followOnce reads one connection of the stream. ended says the task's "done" event came.
func (c *Client) followOnce(ctx context.Context, id int64, lastLine *string, onEvent func(TaskEvent) error) (done ctlapi.Task, ended bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+strings.Replace(ctlapi.RouteTaskEvents, "{id}", strconv.FormatInt(id, 10), 1), nil)
	if err != nil {
		return ctlapi.Task{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/event-stream")
	if *lastLine != "" {
		req.Header.Set("Last-Event-ID", *lastLine)
	}
	streaming := *c.http
	streaming.Timeout = 0
	resp, err := streaming.Do(req)
	if err != nil {
		return ctlapi.Task{}, false, fmt.Errorf("call the control plane: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		answer, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		failure := c.failure(resp, answer)
		var refused *Error
		if errors.As(failure, &refused) && refused.Status < 500 {
			return ctlapi.Task{}, false, stopped{failure} // a 4xx will not be better in a second
		}
		return ctlapi.Task{}, false, failure
	}
	var name, eventID, data string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxEventLine)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "": // the event is complete
			if name == "" {
				continue
			}
			event, err := parseEvent(name, data)
			if err != nil {
				return ctlapi.Task{}, false, stopped{err}
			}
			if eventID != "" {
				*lastLine = eventID
			}
			if err := onEvent(event); err != nil {
				return ctlapi.Task{}, false, stopped{err}
			}
			if event.Kind == ctlapi.EventDone {
				return event.Task, true, nil
			}
			name, eventID, data = "", "", ""
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			eventID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	return ctlapi.Task{}, false, firstError(scanner.Err(), io.ErrUnexpectedEOF)
}

// parseEvent reads the data of an event; the texts in it are cleaned.
func parseEvent(name, data string) (TaskEvent, error) {
	event := TaskEvent{Kind: name}
	var err error
	switch name {
	case ctlapi.EventState:
		var body struct {
			State string `json:"state"`
		}
		err = json.Unmarshal([]byte(data), &body)
		event.State = termtext.Line(body.State)
	case ctlapi.EventLog:
		err = json.Unmarshal([]byte(data), &event.Line)
		cleanLine(&event.Line)
	case ctlapi.EventDone:
		err = json.Unmarshal([]byte(data), &event.Task)
		cleanTask(&event.Task)
	default:
		return TaskEvent{Kind: "unknown"}, nil // a newer control plane may send more kinds
	}
	if err != nil {
		return TaskEvent{}, fmt.Errorf("an event of the control plane is not what it sends: %w", err)
	}
	return event, nil
}
