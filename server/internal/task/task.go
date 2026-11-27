// Package task is the queue of what the control plane asks its agents to do. A person queues a task for
// one agent; the agent, which dials out, takes it with a long poll, runs it if its own allowlist says
// yes, and sends back the result. Every step is written to the audit log. A task nobody takes or
// answers in time expires, so a stale request never runs hours later.
package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/store"
)

// The rules of the queue.
const (
	// QueueTTL is how long a task may wait for its agent to take it.
	QueueTTL = 10 * time.Minute
	// MaxOpen is how many tasks of one agent may be queued or running: a loop of a script cannot fill the table.
	MaxOpen = 20
	// Retention is how long a finished task, with its log, is kept.
	Retention = 30 * 24 * time.Hour
	// claimBatch is the most tasks one poll returns.
	claimBatch = 5
)

var (
	ErrNotFound      = errors.New("no such task")
	ErrAgentNotReady = errors.New("the server has not joined, or was revoked")
	ErrTooMany       = errors.New("too many tasks are waiting for this server")
	ErrNotQueued     = errors.New("the task is not waiting any more: it was sent or has ended")
	ErrNotRunning    = errors.New("the task is not running")
	ErrLogFull       = errors.New("the log of the task is full")
	// ErrInvalid is a task or a result that the table of actions does not allow; the error says what is wrong.
	ErrInvalid = errors.New("invalid")
)

// invalid is an error with the words of err that is also ErrInvalid.
type invalid struct{ error }

func (invalid) Is(target error) bool { return target == ErrInvalid }
func (e invalid) Unwrap() error      { return e.error }

// Task is a row of the queue.
type Task struct {
	ID         int64
	Agent      string
	Action     string
	Params     map[string]string
	State      string
	CreatedAt  time.Time
	CreatedBy  string
	ExpiresAt  time.Time
	SentAt     *time.Time
	ResultBy   *time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Output     string
	Error      string
}

// Line is one line of the log of a task.
type Line struct {
	Seq  int64
	At   time.Time
	Text string
}

// Service is the queue.
type Service struct {
	store *store.Store

	mu    sync.Mutex
	wake  map[string]chan struct{} // an agent's channel, with room for one token: "something was queued"
	polls map[string]*poll         // the long poll each agent has open now
}

type poll struct{ cancel context.CancelFunc }

func New(st *store.Store) *Service {
	return &Service{store: st, wake: map[string]chan struct{}{}, polls: map[string]*poll{}}
}

// Create queues a task for an agent. The action and its parameters are checked against the table of
// agentapi, as the agent will check them again.
func (s *Service) Create(ctx context.Context, agent, action string, params map[string]string) (Task, error) {
	if err := agentapi.ValidateTask(action, params); err != nil {
		return Task{}, invalid{err}
	}
	encoded, err := json.Marshal(nonNil(params))
	if err != nil {
		return Task{}, fmt.Errorf("encode params: %w", err)
	}
	now := time.Now()
	actor := audit.ActorFrom(ctx)
	var id int64
	err = s.store.InTx(ctx, func(tx *sql.Tx) error {
		var state string
		switch err := tx.QueryRowContext(ctx, `SELECT state FROM agents WHERE name = ?`, agent).Scan(&state); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: no server named %q", ErrAgentNotReady, agent)
		case err != nil:
			return fmt.Errorf("read agent: %w", err)
		case state != "active":
			return fmt.Errorf("%w: %s is %s", ErrAgentNotReady, agent, state)
		}
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE agent = ? AND state IN ('queued', 'sent')`, agent).Scan(&open); err != nil {
			return fmt.Errorf("count open tasks: %w", err)
		}
		if open >= MaxOpen {
			return fmt.Errorf("%w (%d): wait for them or cancel some", ErrTooMany, open)
		}
		created, err := tx.ExecContext(ctx, `INSERT INTO tasks (agent, action, params, state, created_at, created_by, expires_at)
			VALUES (?, ?, ?, 'queued', ?, ?, ?)`, agent, action, string(encoded), now.Unix(), createdBy(actor), now.Add(QueueTTL).Unix())
		if err != nil {
			return fmt.Errorf("insert task: %w", err)
		}
		if id, err = created.LastInsertId(); err != nil {
			return fmt.Errorf("task id: %w", err)
		}
		return audit.Append(ctx, tx, now, audit.Entry{Action: "task.create", Target: agent, Detail: describe(id, action, params)})
	})
	if err != nil {
		return Task{}, err
	}
	s.wakeUp(agent)
	return s.Get(ctx, id)
}

func createdBy(actor audit.Actor) string {
	if actor.Session == "" {
		return actor.Name
	}
	return actor.Name + ":" + actor.Session
}

// describe is the task in a line for the audit log: its number, the action and the parameters in order.
func describe(id int64, action string, params map[string]string) string {
	text := fmt.Sprintf("#%d %s", id, action)
	for _, name := range slices.Sorted(maps.Keys(params)) {
		text += fmt.Sprintf(" %s=%q", name, params[name])
	}
	return text
}

func nonNil(params map[string]string) map[string]string {
	if params == nil {
		return map[string]string{}
	}
	return params
}

// Cancel takes back a task that no agent has taken yet.
func (s *Service) Cancel(ctx context.Context, id int64) error {
	now := time.Now()
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		var agent string
		switch err := tx.QueryRowContext(ctx, `SELECT agent FROM tasks WHERE id = ?`, id).Scan(&agent); {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("read task: %w", err)
		}
		changed, err := tx.ExecContext(ctx, `UPDATE tasks SET state = 'canceled', finished_at = ? WHERE id = ? AND state = 'queued'`, now.Unix(), id)
		if err != nil {
			return fmt.Errorf("cancel task: %w", err)
		}
		if count, _ := changed.RowsAffected(); count == 0 {
			return ErrNotQueued
		}
		return audit.Append(ctx, tx, now, audit.Entry{Action: "task.cancel", Target: agent, Detail: fmt.Sprintf("#%d", id)})
	})
}

// Poll returns the tasks that are waiting for the agent, or waits up to wait for one to be queued and
// returns nil if none comes. A new poll of the same agent ends the one before it: an agent that
// reconnects leaves nothing hanging on the server.
func (s *Service) Poll(ctx context.Context, agent string, wait time.Duration) ([]agentapi.Task, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	mine := &poll{cancel: cancel}
	s.mu.Lock()
	if previous := s.polls[agent]; previous != nil {
		previous.cancel()
	}
	s.polls[agent] = mine
	woken := s.wakeChannel(agent)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.polls[agent] == mine {
			delete(s.polls, agent)
		}
		s.mu.Unlock()
	}()
	for {
		tasks, err := s.claim(ctx, agent)
		switch {
		case ctx.Err() != nil: // the poll ended (the agent left, a newer poll replaced it): claim rolled back
			return nil, nil
		case err != nil || len(tasks) > 0:
			return tasks, err
		}
		select {
		case <-woken:
		case <-ctx.Done():
			return nil, nil
		}
	}
}

// wakeChannel is the agent's channel; the caller holds the lock.
func (s *Service) wakeChannel(agent string) chan struct{} {
	channel, found := s.wake[agent]
	if !found {
		channel = make(chan struct{}, 1)
		s.wake[agent] = channel
	}
	return channel
}

func (s *Service) wakeUp(agent string) {
	s.mu.Lock()
	channel := s.wakeChannel(agent)
	s.mu.Unlock()
	select {
	case channel <- struct{}{}:
	default: // a token is waiting already
	}
}

// claim marks the agent's waiting tasks as sent, oldest first, and returns them. It is one transaction, so
// two polls of the same agent can never both get a task.
func (s *Service) claim(ctx context.Context, agent string) ([]agentapi.Task, error) {
	now := time.Now()
	var claimed []agentapi.Task
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		found, err := tx.QueryContext(ctx, `SELECT id, action, params FROM tasks
			WHERE agent = ? AND state = 'queued' AND expires_at > ? ORDER BY id LIMIT ?`, agent, now.Unix(), claimBatch)
		if err != nil {
			return fmt.Errorf("find tasks: %w", err)
		}
		defer found.Close()
		for found.Next() {
			var task agentapi.Task
			var params string
			if err := found.Scan(&task.ID, &task.Action, &params); err != nil {
				return fmt.Errorf("read task: %w", err)
			}
			if err := json.Unmarshal([]byte(params), &task.Params); err != nil {
				return fmt.Errorf("the params of task %d are damaged: %w", task.ID, err)
			}
			task.ResultBy = now.Add(agentapi.Timeout(task.Action) + agentapi.ResultGrace)
			claimed = append(claimed, task)
		}
		if err := found.Err(); err != nil {
			return fmt.Errorf("find tasks: %w", err)
		}
		found.Close() // the same connection runs the updates
		for _, task := range claimed {
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state = 'sent', sent_at = ?, result_by = ? WHERE id = ?`, now.Unix(), task.ResultBy.Unix(), task.ID); err != nil {
				return fmt.Errorf("mark task sent: %w", err)
			}
			agentActor := audit.WithActor(ctx, audit.Actor{Name: "agent:" + agent, IP: audit.ActorFrom(ctx).IP})
			if err := audit.Append(agentActor, tx, now, audit.Entry{Action: "task.send", Target: agent, Detail: describe(task.ID, task.Action, task.Params)}); err != nil {
				return err
			}
		}
		return nil
	})
	return claimed, err
}

// Result records what an agent says of a task it was sent. Only the agent the task was sent to can
// answer it, and only once.
func (s *Service) Result(ctx context.Context, agent string, id int64, result agentapi.TaskResult) error {
	if !agentapi.ValidState(result.State) {
		return invalid{fmt.Errorf("a result is succeeded, failed or refused, not %q", agentapi.CleanText(result.State, 32))}
	}
	result = result.Clean()
	now := time.Now()
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		var sentAt sql.NullInt64
		var state string
		switch err := tx.QueryRowContext(ctx, `SELECT state, sent_at FROM tasks WHERE id = ? AND agent = ?`, id, agent).Scan(&state, &sentAt); {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound // also another agent's task: an agent cannot tell which
		case err != nil:
			return fmt.Errorf("read task: %w", err)
		case state != agentapi.TaskSent:
			return ErrNotRunning
		}
		started := clamp(result.StartedAt, time.Unix(sentAt.Int64, 0), now)
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, started_at = ?, finished_at = ?, output = ?, error = ? WHERE id = ?`,
			result.State, started.Unix(), now.Unix(), result.Output, result.Error, id); err != nil {
			return fmt.Errorf("save result: %w", err)
		}
		return audit.Append(ctx, tx, now, audit.Entry{Action: "task.result", Target: agent, Detail: fmt.Sprintf("#%d %s", id, result.State)})
	})
}

// clamp keeps a time an agent reported, which its clock may have got wrong, between two the control plane knows.
func clamp(t, low, high time.Time) time.Time {
	return time.Unix(min(max(t.Unix(), low.Unix()), high.Unix()), 0)
}

// AppendLog adds a piece of text to the log of a task that is running on the agent. The log of a task is
// capped, so an agent cannot fill the database.
func (s *Service) AppendLog(ctx context.Context, agent string, id int64, text string) error {
	text = agentapi.CleanText(text, agentapi.MaxTaskLogChunk)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	now := time.Now()
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		var state string
		var logged int
		switch err := tx.QueryRowContext(ctx, `SELECT state, log_chars FROM tasks WHERE id = ? AND agent = ?`, id, agent).Scan(&state, &logged); {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("read task: %w", err)
		case state != agentapi.TaskSent:
			return ErrNotRunning
		}
		length := len([]rune(text))
		if logged+length > agentapi.MaxTaskLog {
			return ErrLogFull
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_logs (task, seq, at, text)
			VALUES (?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM task_logs WHERE task = ?), ?, ?)`, id, id, now.Unix(), text); err != nil {
			return fmt.Errorf("save log: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET log_chars = log_chars + ? WHERE id = ?`, length, id); err != nil {
			return fmt.Errorf("count log: %w", err)
		}
		return nil
	})
}

// Expire ends the tasks that nobody took in time and those that were sent and never answered.
func (s *Service) Expire(ctx context.Context, now time.Time) error {
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		found, err := tx.QueryContext(ctx, `SELECT id, agent, state FROM tasks
			WHERE (state = 'queued' AND expires_at <= ?) OR (state = 'sent' AND result_by <= ?)`, now.Unix(), now.Unix())
		if err != nil {
			return fmt.Errorf("find stale tasks: %w", err)
		}
		type stale struct {
			id           int64
			agent, state string
		}
		var tasks []stale
		for found.Next() {
			var task stale
			if err := found.Scan(&task.id, &task.agent, &task.state); err != nil {
				_ = found.Close()
				return fmt.Errorf("read stale task: %w", err)
			}
			tasks = append(tasks, task)
		}
		if err := errors.Join(found.Err(), found.Close()); err != nil {
			return fmt.Errorf("find stale tasks: %w", err)
		}
		for _, task := range tasks {
			reason := "the server did not take it in time"
			if task.state == agentapi.TaskSent {
				reason = "the server sent no result in time"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state = 'expired', finished_at = ?, error = ? WHERE id = ?`, now.Unix(), reason, task.id); err != nil {
				return fmt.Errorf("expire task: %w", err)
			}
			if err := audit.Append(ctx, tx, now, audit.Entry{Action: "task.expire", Target: task.agent, Detail: fmt.Sprintf("#%d %s", task.id, reason)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Prune deletes the tasks that ended more than Retention ago, and their logs.
func (s *Service) Prune(ctx context.Context, now time.Time) error {
	if _, err := s.store.DB.ExecContext(ctx, `DELETE FROM tasks WHERE finished_at IS NOT NULL AND finished_at < ?`, now.Add(-Retention).Unix()); err != nil {
		return fmt.Errorf("prune tasks: %w", err)
	}
	return nil
}

const taskColumns = `id, agent, action, params, state, created_at, created_by, expires_at, sent_at, result_by, started_at, finished_at, output, error`

// Get is one task.
func (s *Service) Get(ctx context.Context, id int64) (Task, error) {
	task, err := scan(s.store.DB.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// List returns the newest tasks first; agent limits it to one server ("" for all).
func (s *Service) List(ctx context.Context, agent string, limit int) ([]Task, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE (? = '' OR agent = ?) ORDER BY id DESC LIMIT ?`, agent, agent, limit)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		task, err := scan(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Task, error) {
	var task Task
	var params string
	var created, expires int64
	var sent, resultBy, started, finished sql.NullInt64
	if err := row.Scan(&task.ID, &task.Agent, &task.Action, &params, &task.State, &created, &task.CreatedBy, &expires,
		&sent, &resultBy, &started, &finished, &task.Output, &task.Error); err != nil {
		return Task{}, err
	}
	if err := json.Unmarshal([]byte(params), &task.Params); err != nil {
		return Task{}, fmt.Errorf("the params of task %d are damaged: %w", task.ID, err)
	}
	task.CreatedAt, task.ExpiresAt = time.Unix(created, 0), time.Unix(expires, 0)
	task.SentAt, task.ResultBy, task.StartedAt, task.FinishedAt = optional(sent), optional(resultBy), optional(started), optional(finished)
	return task, nil
}

func optional(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	t := time.Unix(value.Int64, 0)
	return &t
}

// Log returns the lines of the log of a task after seq (0 for all of them), oldest first.
func (s *Service) Log(ctx context.Context, id, after int64) ([]Line, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT seq, at, text FROM task_logs WHERE task = ? AND seq > ? ORDER BY seq`, id, after)
	if err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	defer rows.Close()
	var lines []Line
	for rows.Next() {
		var line Line
		var at int64
		if err := rows.Scan(&line.Seq, &at, &line.Text); err != nil {
			return nil, fmt.Errorf("read log line: %w", err)
		}
		line.At = time.Unix(at, 0)
		lines = append(lines, line)
	}
	return lines, rows.Err()
}

// Run keeps the queue tidy until ctx is done: it expires what is stale every half minute and deletes
// what is old every hour.
func (s *Service) Run(ctx context.Context) error {
	expire, prune := time.NewTicker(30*time.Second), time.NewTicker(time.Hour)
	defer expire.Stop()
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-expire.C:
			if err := s.Expire(ctx, now); err != nil && ctx.Err() == nil {
				slog.Error("expire tasks", "err", err)
			}
		case now := <-prune.C:
			if err := s.Prune(ctx, now); err != nil && ctx.Err() == nil {
				slog.Error("prune tasks", "err", err)
			}
		}
	}
}
