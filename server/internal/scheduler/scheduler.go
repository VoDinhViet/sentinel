// Package scheduler gives tasks a timetable and the agents a maintenance window. A schedule is a task that is
// queued by the control plane itself, by a cron timetable, for one agent; it goes through the same table of
// actions and the same queue as a task a person gives, and the agent's own allowlist still has the last word.
// A maintenance window says that an agent (or all of them) is away on purpose: the offline alert stays quiet
// and sentinelctl shows MAINTENANCE.
package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/termtext"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/store"
	"sentinel/server/internal/task"
)

// The rules of the timetable.
const (
	// MaxSchedules is how many schedules there are, in all; MaxPerAgent is how many one agent has.
	MaxSchedules = 200
	MaxPerAgent  = 10 // each schedule has one task open at most (see run), so the other half of the queue is left to people
	// CatchUp is how late a run may be and still be queued: a control plane that was down over a run does not
	// start it afterwards, because the task would be stale by then (the same ten minutes a task may wait).
	CatchUp = task.QueueTTL
	// Every is how often the timetables are looked at.
	Every = 20 * time.Second
	// MaxWindows is how many maintenance windows are kept that have not ended; MaxWindow how long one may be.
	MaxWindows = 200
	MaxWindow  = 30 * 24 * time.Hour
	// MaxAhead is how far from now a window may start.
	MaxAhead = 366 * 24 * time.Hour
	// MaxReason is the longest note on a window, in characters.
	MaxReason = 200
	// windowKeep is how long a window that has ended is kept before it is deleted.
	windowKeep = 30 * 24 * time.Hour
)

var (
	ErrNotFound = errors.New("not found")
	ErrTooMany  = errors.New("too many")
	// ErrInvalid is a schedule or a window that cannot be made; the error says what is wrong.
	ErrInvalid = errors.New("invalid")
	// ErrNoAgent is a name that is not a server of this control plane.
	ErrNoAgent = errors.New("no such server")
)

// invalid is an error with the words of err that is also ErrInvalid.
type invalid struct{ error }

func (invalid) Is(target error) bool { return target == ErrInvalid }
func (e invalid) Unwrap() error      { return e.error }

// Schedule is a row of the timetable.
type Schedule struct {
	ID        int64
	Agent     string
	Action    string
	Params    map[string]string
	Spec      string
	CreatedAt time.Time
	CreatedBy string
	LastRunAt *time.Time
	LastTask  *int64
	LastError string
}

// Next is when the schedule runs next, after now.
func (s Schedule) Next(now time.Time) (time.Time, bool) {
	cron, err := ParseCron(s.Spec)
	if err != nil {
		return time.Time{}, false
	}
	return cron.Next(now)
}

// Window is a maintenance window. Agent is empty for all of them.
type Window struct {
	ID        int64
	Agent     string
	StartsAt  time.Time
	EndsAt    time.Time
	Reason    string
	CreatedAt time.Time
	CreatedBy string
}

// Queue is what the scheduler needs of the task queue.
type Queue interface {
	Create(ctx context.Context, agent, action string, params map[string]string) (task.Task, error)
}

// Service keeps the schedules and the windows, and queues the tasks that are due.
type Service struct {
	store *store.Store
	queue Queue
	// Now is the time; nil means the clock.
	Now func() time.Time
}

func New(st *store.Store, queue Queue) *Service { return &Service{store: st, queue: queue} }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// AddSchedule makes a schedule for an agent. The action and its parameters are checked against the table of
// actions now, and again every time a task is queued from it.
func (s *Service) AddSchedule(ctx context.Context, agent, spec, action string, params map[string]string) (Schedule, error) {
	if _, err := ParseCron(spec); err != nil {
		return Schedule{}, invalid{fmt.Errorf("the timetable: %w", err)}
	}
	if err := agentapi.ValidateTask(action, params); err != nil {
		return Schedule{}, invalid{err}
	}
	encoded, err := json.Marshal(nonNil(params))
	if err != nil {
		return Schedule{}, fmt.Errorf("encode params: %w", err)
	}
	now, actor := s.now(), audit.ActorFrom(ctx)
	var id int64
	err = s.store.InTx(ctx, func(tx *sql.Tx) error {
		if err := agentIsActive(ctx, tx, agent); err != nil {
			return err
		}
		var all, ofAgent int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(agent = ?), 0) FROM schedules`, agent).Scan(&all, &ofAgent); err != nil {
			return fmt.Errorf("count schedules: %w", err)
		}
		if all >= MaxSchedules || ofAgent >= MaxPerAgent {
			return fmt.Errorf("%w schedules (%d for this server, %d in all): remove some first", ErrTooMany, ofAgent, all)
		}
		created, err := tx.ExecContext(ctx, `INSERT INTO schedules (agent, action, params, spec, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
			agent, action, string(encoded), spec, now.Unix(), createdBy(actor))
		if err != nil {
			return fmt.Errorf("insert schedule: %w", err)
		}
		if id, err = created.LastInsertId(); err != nil {
			return fmt.Errorf("schedule id: %w", err)
		}
		return audit.Append(ctx, tx, now, audit.Entry{Action: "schedule.create", Target: agent, Detail: describe(id, spec, action, params)})
	})
	if err != nil {
		return Schedule{}, err
	}
	return s.Schedule(ctx, id)
}

// agentIsActive says the agent is one that has joined and is not revoked.
func agentIsActive(ctx context.Context, q store.Querier, agent string) error {
	var state string
	switch err := q.QueryRowContext(ctx, `SELECT state FROM agents WHERE name = ?`, agent).Scan(&state); {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: %q", ErrNoAgent, termtext.Shorten(termtext.Line(agent), 64))
	case err != nil:
		return fmt.Errorf("read agent: %w", err)
	case state == "revoked":
		return fmt.Errorf("%w: %s was revoked", ErrNoAgent, agent)
	}
	return nil
}

func createdBy(actor audit.Actor) string {
	if actor.Session == "" {
		return actor.Name
	}
	return actor.Name + ":" + actor.Session
}

func describe(id int64, spec, action string, params map[string]string) string {
	text := fmt.Sprintf("#%d %q %s", id, spec, action)
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

const scheduleColumns = `id, agent, action, params, spec, created_at, created_by, last_run_at, last_task, last_error`

type scanner interface{ Scan(dest ...any) error }

func scanSchedule(row scanner) (Schedule, error) {
	var (
		found               Schedule
		params              string
		createdAt           int64
		lastRunAt, lastTask sql.NullInt64
	)
	if err := row.Scan(&found.ID, &found.Agent, &found.Action, &params, &found.Spec, &createdAt, &found.CreatedBy, &lastRunAt, &lastTask, &found.LastError); err != nil {
		return Schedule{}, err
	}
	if err := json.Unmarshal([]byte(params), &found.Params); err != nil {
		return Schedule{}, fmt.Errorf("the parameters of schedule %d are damaged: %w", found.ID, err)
	}
	found.CreatedAt = time.Unix(createdAt, 0)
	if lastRunAt.Valid {
		at := time.Unix(lastRunAt.Int64, 0)
		found.LastRunAt = &at
	}
	if lastTask.Valid {
		found.LastTask = &lastTask.Int64
	}
	return found, nil
}

// Schedule returns one schedule.
func (s *Service) Schedule(ctx context.Context, id int64) (Schedule, error) {
	found, err := scanSchedule(s.store.DB.QueryRowContext(ctx, `SELECT `+scheduleColumns+` FROM schedules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, fmt.Errorf("%w: no schedule #%d", ErrNotFound, id)
	}
	return found, err
}

// Schedules returns all of them, or those of one agent, in order of number.
func (s *Service) Schedules(ctx context.Context, agent string) ([]Schedule, error) {
	query, args := `SELECT `+scheduleColumns+` FROM schedules ORDER BY id`, []any(nil)
	if agent != "" {
		query, args = `SELECT `+scheduleColumns+` FROM schedules WHERE agent = ? ORDER BY id`, []any{agent}
	}
	rows, err := s.store.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()
	var found []Schedule
	for rows.Next() {
		one, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, one)
	}
	return found, rows.Err()
}

// RemoveSchedule deletes a schedule. A task it already queued stays.
func (s *Service) RemoveSchedule(ctx context.Context, id int64) error {
	now := s.now()
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		var agent string
		if err := tx.QueryRowContext(ctx, `SELECT agent FROM schedules WHERE id = ?`, id).Scan(&agent); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no schedule #%d", ErrNotFound, id)
		} else if err != nil {
			return fmt.Errorf("read schedule: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete schedule: %w", err)
		}
		return audit.Append(ctx, tx, now, audit.Entry{Action: "schedule.delete", Target: agent, Detail: "#" + strconv.FormatInt(id, 10)})
	})
}

// dueAt is the latest time the timetable names that is after since and not after now, and false if there is none.
// A run that is older than CatchUp is not looked for: it counts as missed.
func dueAt(cron Cron, since, now time.Time) (time.Time, bool) {
	from := since
	if floor := now.Add(-CatchUp); from.Before(floor) {
		from = floor
	}
	var due time.Time
	for {
		next, ok := cron.Next(from)
		if !ok || next.After(now) {
			return due, !due.IsZero()
		}
		due, from = next, next
	}
}

// Tick queues the task of every schedule that is due at now. A schedule that was due more than once since the
// last look (the control plane was away, a run took long) is queued once. A run is claimed before its task is
// queued, so a stop in between loses the run and never doubles it. A run is also skipped while the task the
// schedule queued last time is still waiting or running: a server that is down does not collect a task a minute.
// A server that has not joined or was revoked gets nothing and nothing is written about it.
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	schedules, err := s.Schedules(ctx, "")
	if err != nil {
		return err
	}
	states, err := s.agentStates(ctx)
	if err != nil {
		return err
	}
	for _, one := range schedules {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		cron, err := ParseCron(one.Spec)
		if err != nil {
			slog.Error("schedule: the timetable cannot be read", "schedule", one.ID, "err", err)
			continue
		}
		since := one.CreatedAt
		if one.LastRunAt != nil {
			since = *one.LastRunAt
		}
		due, ok := dueAt(cron, since, now)
		if !ok || states[one.Agent] != "active" {
			continue
		}
		if err := s.run(ctx, one, due, now); err != nil {
			slog.Error("schedule: run", "schedule", one.ID, "err", err)
		}
	}
	return nil
}

func (s *Service) agentStates(ctx context.Context) (map[string]string, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT name, state FROM agents`)
	if err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}
	defer rows.Close()
	states := map[string]string{}
	for rows.Next() {
		var name, state string
		if err := rows.Scan(&name, &state); err != nil {
			return nil, fmt.Errorf("read agent: %w", err)
		}
		states[name] = state
	}
	return states, rows.Err()
}

// taskIsOpen says the task is still queued or sent.
func (s *Service) taskIsOpen(ctx context.Context, id int64) (bool, error) {
	var open int
	err := s.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = ? AND state IN ('queued', 'sent')`, id).Scan(&open)
	return open > 0, err
}

// run claims a run that is due, queues its task and keeps how that went.
func (s *Service) run(ctx context.Context, one Schedule, due, now time.Time) error {
	if one.LastTask != nil {
		if open, err := s.taskIsOpen(ctx, *one.LastTask); err != nil {
			return err
		} else if open {
			_, err := s.claim(ctx, one.ID, due) // the run is passed over, not kept for later
			return err
		}
	}
	if won, err := s.claim(ctx, one.ID, due); err != nil || !won {
		return err
	}
	actor := audit.Actor{Name: "schedule", Session: strconv.FormatInt(one.ID, 10)}
	runCtx := audit.WithActor(ctx, actor)
	queued, queueErr := s.queue.Create(runCtx, one.Agent, one.Action, one.Params)
	if queueErr == nil {
		_, err := s.store.DB.ExecContext(ctx, `UPDATE schedules SET last_task = ?, last_error = '' WHERE id = ?`, queued.ID, one.ID)
		return err
	}
	lastError := termtext.Shorten(termtext.Line(queueErr.Error()), 200)
	return s.store.InTx(runCtx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE schedules SET last_task = NULL, last_error = ? WHERE id = ?`, lastError, one.ID); err != nil {
			return fmt.Errorf("update schedule: %w", err)
		}
		return audit.Append(runCtx, tx, now, audit.Entry{Action: "schedule.run", Target: one.Agent, Detail: "#" + strconv.FormatInt(one.ID, 10) + " not queued: " + lastError})
	})
}

// claim records that the run due at due is taken, and says whether this call took it: a run at or after it that
// was taken already (by another control plane on the same database, or an earlier look) is not taken twice.
func (s *Service) claim(ctx context.Context, id int64, due time.Time) (bool, error) {
	claimed, err := s.store.DB.ExecContext(ctx, `UPDATE schedules SET last_run_at = ? WHERE id = ? AND (last_run_at IS NULL OR last_run_at < ?)`, due.Unix(), id, due.Unix())
	if err != nil {
		return false, fmt.Errorf("claim a run: %w", err)
	}
	changed, err := claimed.RowsAffected()
	return changed > 0, err
}

// Run looks at the timetables every Every until ctx is done, and deletes windows that ended long ago.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(Every)
	defer ticker.Stop()
	for {
		now := s.now()
		if err := s.Tick(ctx, now); err != nil && ctx.Err() == nil {
			slog.Error("schedule: tick", "err", err)
		}
		if err := s.pruneWindows(ctx, now); err != nil && ctx.Err() == nil {
			slog.Error("maintenance: prune", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
