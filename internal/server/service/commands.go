package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"sentinel/internal/api"
)

type CommandRecord struct {
	ID         int64
	AgentID    int64
	Type       string
	Args       json.RawMessage
	Status     string
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Output     json.RawMessage
	Error      string
}

// Done reports whether the command reached a final state.
func (c CommandRecord) Done() bool {
	return c.Status != api.StatusPending && c.Status != api.StatusRunning
}

// Describe is a one-line human summary.
func (c CommandRecord) Describe() string {
	switch c.Type {
	case api.CmdBackupRun:
		var a api.BackupRunArgs
		json.Unmarshal(c.Args, &a)
		return "Backup " + a.Job
	case api.CmdSnapshots:
		var a api.SnapshotsArgs
		json.Unmarshal(c.Args, &a)
		return fmt.Sprintf("List snapshots of %s in %s", a.Job, a.Repository)
	case api.CmdRestore:
		var a api.RestoreArgs
		json.Unmarshal(c.Args, &a)
		return fmt.Sprintf("Restore %s snapshot %.8s from %s", a.Job, a.Snapshot, a.Repository)
	}
	return c.Type
}

var snapshotID = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// validateCommand checks a command against the jobs the agent last reported.
func validateCommand(a Agent, typ string, args any) error {
	findJob := func(name string) (api.Job, error) {
		for _, j := range a.Jobs {
			if j.Name == name {
				return j, nil
			}
		}
		return api.Job{}, fmt.Errorf("agent %s has no backup job %q", a.Name, name)
	}
	checkRepo := func(job, repo string) error {
		j, err := findJob(job)
		if err != nil {
			return err
		}
		if !slices.Contains(j.Repositories, repo) {
			return fmt.Errorf("job %q does not use repository %q", job, repo)
		}
		return nil
	}
	switch v := args.(type) {
	case api.BackupRunArgs:
		_, err := findJob(v.Job)
		return err
	case api.SnapshotsArgs:
		return checkRepo(v.Job, v.Repository)
	case api.RestoreArgs:
		if !snapshotID.MatchString(v.Snapshot) {
			return fmt.Errorf("invalid snapshot id %q", v.Snapshot)
		}
		return checkRepo(v.Job, v.Repository)
	}
	return fmt.Errorf("unknown command type %q", typ)
}

func commandType(args any) string {
	switch args.(type) {
	case api.BackupRunArgs:
		return api.CmdBackupRun
	case api.SnapshotsArgs:
		return api.CmdSnapshots
	case api.RestoreArgs:
		return api.CmdRestore
	}
	return ""
}

// Enqueue queues a command for an agent. args is one of the api.*Args types.
func (s *Service) Enqueue(ctx context.Context, agentID int64, args any) (int64, error) {
	a, err := s.GetAgent(ctx, agentID)
	if err != nil {
		return 0, err
	}
	typ := commandType(args)
	if err := validateCommand(a, typ, args); err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(args)
	res, err := s.db.ExecContext(ctx, `INSERT INTO commands (agent_id, type, args, status, created_at)
		VALUES (?, ?, ?, ?, ?)`, agentID, typ, string(raw), api.StatusPending, s.now().Unix())
	if err != nil {
		return 0, err
	}
	s.wake(agentID)
	return res.LastInsertId()
}

// wakeChan returns the channel an agent's long-poll waits on.
func (s *Service) wakeChan(agentID int64) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.wakeups[agentID]
	if !ok {
		ch = make(chan struct{}, 1)
		s.wakeups[agentID] = ch
	}
	return ch
}

func (s *Service) wake(agentID int64) {
	select {
	case s.wakeChan(agentID) <- struct{}{}:
	default:
	}
}

// WaitCommands returns pending commands for the agent, marking them running.
// If there are none it waits up to wait for one to be queued.
func (s *Service) WaitCommands(ctx context.Context, agentID int64, wait time.Duration) ([]api.Command, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		cmds, err := s.claim(ctx, agentID)
		if err != nil || len(cmds) > 0 {
			return cmds, err
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-deadline.C:
			return nil, nil
		case <-s.wakeChan(agentID):
		}
	}
}

func (s *Service) claim(ctx context.Context, agentID int64) ([]api.Command, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, type, args FROM commands
		WHERE agent_id = ? AND status = ? ORDER BY id`, agentID, api.StatusPending)
	if err != nil {
		return nil, err
	}
	var cmds []api.Command
	for rows.Next() {
		var c api.Command
		var args string
		if err := rows.Scan(&c.ID, &c.Type, &args); err != nil {
			rows.Close()
			return nil, err
		}
		c.Args = json.RawMessage(args)
		cmds = append(cmds, c)
	}
	rows.Close()
	for _, c := range cmds {
		if _, err := tx.ExecContext(ctx, `UPDATE commands SET status = ?, started_at = ? WHERE id = ?`,
			api.StatusRunning, s.now().Unix(), c.ID); err != nil {
			return nil, err
		}
	}
	return cmds, tx.Commit()
}

// Complete records a command's result. Only the owning agent may complete
// it, and only while it is running.
func (s *Service) Complete(ctx context.Context, agentID, cmdID int64, r api.CommandResult) error {
	status := api.StatusDone
	if !r.OK {
		status = api.StatusFailed
	}
	if len(r.Output) > 1<<20 {
		return errors.New("command output too large")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE commands SET status = ?, finished_at = ?, output = ?, error = ?
		WHERE id = ? AND agent_id = ? AND status = ?`,
		status, s.now().Unix(), string(r.Output), r.Error, cmdID, agentID, api.StatusRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const commandCols = `id, agent_id, type, args, status, created_at, started_at, finished_at, output, error`

func scanCommand(row interface{ Scan(...any) error }) (CommandRecord, error) {
	var c CommandRecord
	var args, output string
	var created, started, finished int64
	if err := row.Scan(&c.ID, &c.AgentID, &c.Type, &args, &c.Status, &created, &started, &finished, &output, &c.Error); err != nil {
		return c, err
	}
	c.Args, c.CreatedAt = json.RawMessage(args), time.Unix(created, 0)
	if output != "" {
		c.Output = json.RawMessage(output)
	}
	if started > 0 {
		c.StartedAt = time.Unix(started, 0)
	}
	if finished > 0 {
		c.FinishedAt = time.Unix(finished, 0)
	}
	return c, nil
}

func (s *Service) GetCommand(ctx context.Context, agentID, cmdID int64) (CommandRecord, error) {
	c, err := scanCommand(s.db.QueryRowContext(ctx, `SELECT `+commandCols+` FROM commands
		WHERE id = ? AND agent_id = ?`, cmdID, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListCommands returns an agent's most recent commands, newest first.
func (s *Service) ListCommands(ctx context.Context, agentID int64, limit int) ([]CommandRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+commandCols+` FROM commands WHERE agent_id = ?
		ORDER BY id DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommandRecord
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ExpireCommands times out commands that ran longer than runningFor or were
// never picked up within pendingFor (agent offline).
func (s *Service) ExpireCommands(ctx context.Context, runningFor, pendingFor time.Duration) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx, `UPDATE commands SET status = ?, finished_at = ?, error = ?
		WHERE (status = ? AND started_at < ?) OR (status = ? AND created_at < ?)`,
		api.StatusTimeout, now.Unix(), "no result from agent in time",
		api.StatusRunning, now.Add(-runningFor).Unix(), api.StatusPending, now.Add(-pendingFor).Unix())
	return err
}
