package transport

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"sentinel/pkg/agentapi"
)

// PollWait is how long the control plane may hold a poll for tasks before it answers with none.
const PollWait = 25 * time.Second

// Poll asks the control plane for the tasks waiting for this agent. It waits up to PollWait for one.
func (c *Client) Poll(ctx context.Context) ([]agentapi.Task, error) {
	var tasks []agentapi.Task
	err := call(ctx, c.poll, "GET", c.where.base+agentapi.RouteTasks+"?wait="+strconv.Itoa(int(PollWait.Seconds()))+"s", nil, &tasks)
	return tasks, err
}

func taskRoute(route string, id int64) string {
	return strings.Replace(route, "{id}", strconv.FormatInt(id, 10), 1)
}

// SendResult tells the control plane how a task ended.
func (c *Client) SendResult(ctx context.Context, id int64, result agentapi.TaskResult) error {
	return call(ctx, c.http, "POST", c.where.base+taskRoute(agentapi.RouteTaskResult, id), result, nil)
}

// SendLog adds a piece to the log of a running task.
func (c *Client) SendLog(ctx context.Context, id int64, text string) error {
	return call(ctx, c.http, "POST", c.where.base+taskRoute(agentapi.RouteTaskLog, id), agentapi.TaskLog{Text: text}, nil)
}

// TaskClient is the part of a Client the worker uses.
type TaskClient interface {
	Poll(ctx context.Context) ([]agentapi.Task, error)
	SendResult(ctx context.Context, id int64, result agentapi.TaskResult) error
	SendLog(ctx context.Context, id int64, text string) error
	Reload() error
}

// Runner runs one task and says how it ended. log passes a line of progress to the control plane while
// the task runs; it may be called from the runner's goroutine only, and drops what the control plane
// does not take.
type Runner interface {
	Run(ctx context.Context, task agentapi.Task, log func(line string)) agentapi.TaskResult
}

// What the worker does about time.
const (
	// pollFailPause is how long to wait after a poll that failed, doubling to pollFailMax.
	pollFailPause = 5 * time.Second
	pollFailMax   = time.Minute
	// shortPoll is an answer that came so soon that the control plane did not hold the poll: the worker
	// waits before the next, so a server that answers at once does not turn the loop into a busy one.
	shortPoll = 2 * time.Second
	// resultPause and resultTries: a result is sent again when the control plane could not be reached,
	// until the control plane gives up waiting for it (the task's result_by) or the daemon stops.
	resultPause = 2 * time.Second
	resultMax   = 30 * time.Second
	// logEvery is how often the lines a task has logged are sent; logPiece is the most characters in one call.
	logEvery = time.Second
	logPiece = 8 << 10
	// logTimeout bounds one call with log: it is progress, never worth holding a task for.
	logTimeout = 5 * time.Second
	// finalSend is how long a result gets when the daemon is stopping.
	finalSend = 5 * time.Second
)

// Worker takes tasks from the control plane one at a time and runs them. Like the Reporter, it never
// stops the daemon: with no control plane the agent works alone.
type Worker struct {
	Client TaskClient
	Runner Runner
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Loop polls and runs tasks until ctx is done.
func (w *Worker) Loop(ctx context.Context) {
	failPause, failing, refused := pollFailPause, false, false
	for ctx.Err() == nil {
		if refused {
			refused = false
			if err := w.Client.Reload(); err != nil {
				slog.Warn("control plane: cannot read the identity again", "err", err)
			}
		}
		started := time.Now()
		tasks, err := w.Client.Poll(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case IsStatus(err, 401):
			slog.Error("control plane: it refuses this agent's certificate; no task is asked for until it is accepted", "err", err)
			refused = true
			sleep(ctx, jitter(refusedPause))
			continue
		case err != nil:
			if !failing {
				slog.Warn("control plane: cannot ask for tasks; it is tried again", "err", err)
			}
			failing = true
			sleep(ctx, jitter(failPause))
			failPause = min(failPause*2, pollFailMax)
			continue
		}
		if failing {
			slog.Info("control plane: asking for tasks again")
		}
		failing, failPause = false, pollFailPause
		for _, task := range tasks {
			w.handle(ctx, task)
		}
		if len(tasks) == 0 && time.Since(started) < shortPoll {
			sleep(ctx, shortPoll)
		}
	}
}

// sleep waits for d or for ctx.
func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// handle runs a task and sends its result. The lines the task logs are sent in batches, so a task that
// says a lot does not use up the control plane's limit on calls and make its own result wait.
func (w *Worker) handle(ctx context.Context, task agentapi.Task) {
	batch := &logBatch{}
	flushed := make(chan struct{})
	stop := make(chan struct{})
	go func() {
		defer close(flushed)
		ticker := time.NewTicker(logEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				w.sendLog(ctx, task.ID, batch.take())
			case <-stop:
				return
			}
		}
	}()
	result := w.Runner.Run(ctx, task, batch.add)
	close(stop)
	<-flushed
	w.sendLog(ctx, task.ID, batch.take())
	w.sendResult(ctx, task, result)
}

// sendLog sends the lines in pieces the control plane accepts, and drops what it does not take.
func (w *Worker) sendLog(ctx context.Context, id int64, lines []string) {
	for len(lines) > 0 {
		size, count := 0, 0
		for count < len(lines) && (count == 0 || size+len(lines[count]) < logPiece) {
			size += len(lines[count]) + 1
			count++
		}
		logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), logTimeout)
		err := w.Client.SendLog(logCtx, id, strings.Join(lines[:count], "\n"))
		cancel()
		if err != nil {
			slog.Debug("control plane: a piece of the log of a task was not taken", "task", id, "err", err)
			return
		}
		lines = lines[count:]
	}
}

// logBatch holds the lines a task has logged and the control plane has not been sent yet.
type logBatch struct {
	mu    sync.Mutex
	lines []string
}

func (b *logBatch) add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
}

// take returns the lines held and empties the batch.
func (b *logBatch) take() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := b.lines
	b.lines = nil
	return lines
}

// sendResult tells the control plane how the task ended, again and again while it cannot be reached, until
// the control plane stops waiting for it. An answer that says the task is not waiting for a result (it
// expired, or it is not this agent's) ends the tries: nothing will ever take it.
func (w *Worker) sendResult(ctx context.Context, task agentapi.Task, result agentapi.TaskResult) {
	pause := resultPause
	for {
		sendCtx, cancel := ctx, context.CancelFunc(func() {})
		if ctx.Err() != nil { // the daemon is stopping: one last try, with a clock of its own
			sendCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), finalSend)
		}
		err := w.Client.SendResult(sendCtx, task.ID, result)
		cancel()
		switch {
		case err == nil:
			return
		case IsStatus(err, 404), IsStatus(err, 409):
			slog.Warn("control plane: it does not wait for the result of this task any more", "task", task.ID, "err", err)
			return
		case IsStatus(err, 401), ctx.Err() != nil:
			slog.Error("control plane: the result of a task was not delivered", "task", task.ID, "state", result.State, "err", err)
			return
		case !task.ResultBy.IsZero() && w.now().After(task.ResultBy):
			slog.Error("control plane: the result of a task was not delivered in time", "task", task.ID, "state", result.State, "err", err)
			return
		}
		slog.Warn("control plane: cannot send the result of a task; it is tried again", "task", task.ID, "err", err)
		sleep(ctx, pause)
		pause = min(pause*2, resultMax)
	}
}
