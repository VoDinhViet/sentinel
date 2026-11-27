package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"sentinel/pkg/termtext"
	"sentinel/server/internal/audit"
)

// Active is the agents that are in a maintenance window at one moment.
type Active struct {
	all    bool
	agents map[string]bool
}

// Covers says the agent is in a window now.
func (a Active) Covers(agent string) bool { return a.all || a.agents[agent] }

// ActiveAt reads the windows that are open at now.
func (s *Service) ActiveAt(ctx context.Context, now time.Time) (Active, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT agent FROM maintenance WHERE starts_at <= ? AND ends_at > ?`, now.Unix(), now.Unix())
	if err != nil {
		return Active{}, fmt.Errorf("read maintenance windows: %w", err)
	}
	defer rows.Close()
	active := Active{agents: map[string]bool{}}
	for rows.Next() {
		var agent sql.NullString
		if err := rows.Scan(&agent); err != nil {
			return Active{}, fmt.Errorf("read maintenance window: %w", err)
		}
		if agent.Valid {
			active.agents[agent.String] = true
		} else {
			active.all = true
		}
	}
	return active, rows.Err()
}

// AddWindow opens a window from start to end for one agent, or for all with an empty name.
func (s *Service) AddWindow(ctx context.Context, agent string, start, end time.Time, reason string) (Window, error) {
	now, actor := s.now(), audit.ActorFrom(ctx)
	reason = termtext.Shorten(termtext.Line(reason), MaxReason)
	switch {
	case !end.After(start):
		return Window{}, invalid{errors.New("a window must end after it starts")}
	case !end.After(now):
		return Window{}, invalid{errors.New("the window is over already")}
	case start.After(now.Add(MaxAhead)):
		return Window{}, invalid{errors.New("a window may start at most a year from now")}
	case end.Sub(start) > MaxWindow:
		return Window{}, invalid{fmt.Errorf("a window is at most %d days", int(MaxWindow/(24*time.Hour)))}
	}
	var id int64
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		if agent != "" {
			if err := agentIsActive(ctx, tx, agent); err != nil {
				return err
			}
		}
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM maintenance WHERE ends_at > ?`, now.Unix()).Scan(&open); err != nil {
			return fmt.Errorf("count windows: %w", err)
		}
		if open >= MaxWindows {
			return fmt.Errorf("%w windows (%d): remove some first", ErrTooMany, open)
		}
		var target any
		if agent != "" {
			target = agent
		}
		created, err := tx.ExecContext(ctx, `INSERT INTO maintenance (agent, starts_at, ends_at, reason, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?)`,
			target, start.Unix(), end.Unix(), reason, now.Unix(), createdBy(actor))
		if err != nil {
			return fmt.Errorf("insert window: %w", err)
		}
		if id, err = created.LastInsertId(); err != nil {
			return fmt.Errorf("window id: %w", err)
		}
		name := agent
		if name == "" {
			name = "(all)"
		}
		return audit.Append(ctx, tx, now, audit.Entry{Action: "maintenance.create", Target: name,
			Detail: fmt.Sprintf("#%d from %s to %s %q", id, start.Format(time.RFC3339), end.Format(time.RFC3339), reason)})
	})
	if err != nil {
		return Window{}, err
	}
	return s.Window(ctx, id)
}

const windowColumns = `id, agent, starts_at, ends_at, reason, created_at, created_by`

func scanWindow(row scanner) (Window, error) {
	var (
		found                    Window
		agent                    sql.NullString
		startsAt, endsAt, madeAt int64
	)
	if err := row.Scan(&found.ID, &agent, &startsAt, &endsAt, &found.Reason, &madeAt, &found.CreatedBy); err != nil {
		return Window{}, err
	}
	found.Agent = agent.String
	found.StartsAt, found.EndsAt, found.CreatedAt = time.Unix(startsAt, 0), time.Unix(endsAt, 0), time.Unix(madeAt, 0)
	return found, nil
}

// Window returns one window.
func (s *Service) Window(ctx context.Context, id int64) (Window, error) {
	found, err := scanWindow(s.store.DB.QueryRowContext(ctx, `SELECT `+windowColumns+` FROM maintenance WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Window{}, fmt.Errorf("%w: no maintenance window #%d", ErrNotFound, id)
	}
	return found, err
}

// Windows returns the windows that are open or still to come, soonest first.
func (s *Service) Windows(ctx context.Context, now time.Time) ([]Window, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT `+windowColumns+` FROM maintenance WHERE ends_at > ? ORDER BY starts_at, id`, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("list windows: %w", err)
	}
	defer rows.Close()
	var found []Window
	for rows.Next() {
		one, err := scanWindow(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, one)
	}
	return found, rows.Err()
}

// RemoveWindow deletes a window, which ends it at once if it is open.
func (s *Service) RemoveWindow(ctx context.Context, id int64) error {
	now := s.now()
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		var agent sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT agent FROM maintenance WHERE id = ?`, id).Scan(&agent); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no maintenance window #%d", ErrNotFound, id)
		} else if err != nil {
			return fmt.Errorf("read window: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM maintenance WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete window: %w", err)
		}
		name := agent.String
		if !agent.Valid {
			name = "(all)"
		}
		return audit.Append(ctx, tx, now, audit.Entry{Action: "maintenance.delete", Target: name, Detail: "#" + strconv.FormatInt(id, 10)})
	})
}

func (s *Service) pruneWindows(ctx context.Context, now time.Time) error {
	if _, err := s.store.DB.ExecContext(ctx, `DELETE FROM maintenance WHERE ends_at < ?`, now.Add(-windowKeep).Unix()); err != nil {
		return fmt.Errorf("delete old windows: %w", err)
	}
	return nil
}
