package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"sentinel/internal/api"
	"sentinel/internal/store"
)

// Ingest stores an agent report and returns the cursor the agent should send
// from next time. Rows are keyed by the agent's own ids, so a resent batch is
// harmless.
func (s *Service) Ingest(ctx context.Context, agentID int64, r api.Report) (api.Cursor, error) {
	if len(r.BackupRuns) > api.MaxBatch || len(r.Results) > api.MaxBatch || len(r.Metrics) > api.MaxBatch {
		return api.Cursor{}, fmt.Errorf("batch too large (max %d rows per kind)", api.MaxBatch)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.Cursor{}, err
	}
	defer tx.Rollback()

	jobs, _ := json.Marshal(nonNil(r.Jobs))
	checks, _ := json.Marshal(nonNil(r.Checks))
	res, err := tx.ExecContext(ctx, `UPDATE agents SET last_seen = ?, hostname = ?, version = ?,
		jobs_json = ?, checks_json = ?, running_config = ? WHERE id = ?`,
		s.now().Unix(), r.Hostname, r.Version, string(jobs), string(checks), r.ConfigVersion, agentID)
	if err != nil {
		return api.Cursor{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return api.Cursor{}, ErrNotFound
	}

	for _, b := range r.BackupRuns {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO backup_runs
			(agent_id, remote_id, job, repository, started_at, finished_at, success, snapshot_id, bytes_added, total_bytes, error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			agentID, b.ID, b.Job, b.Repository, b.StartedAt.Unix(), b.FinishedAt.Unix(), b.Success,
			b.SnapshotID, b.BytesAdded, b.TotalBytes, b.Error)
		if err != nil {
			return api.Cursor{}, err
		}
	}
	for _, c := range r.Results {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO check_results
			(agent_id, remote_id, name, at, ok, latency_ms, error) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			agentID, c.ID, c.Name, c.At.Unix(), c.OK, c.Latency.Milliseconds(), c.Error)
		if err != nil {
			return api.Cursor{}, err
		}
	}
	for _, m := range r.Metrics {
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO metrics
			(agent_id, remote_id, at, cpu, memory, swap, load1, disk_max) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			agentID, m.ID, m.At.Unix(), m.CPU, m.Memory, m.Swap, m.Load1, m.DiskMax)
		if err != nil {
			return api.Cursor{}, err
		}
	}

	// Open alerts are a full snapshot: replace.
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_alerts WHERE agent_id = ?`, agentID); err != nil {
		return api.Cursor{}, err
	}
	for _, a := range r.OpenAlerts {
		_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agent_alerts (agent_id, key, since, message) VALUES (?, ?, ?, ?)`,
			agentID, a.Key, a.Since.Unix(), a.Message)
		if err != nil {
			return api.Cursor{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return api.Cursor{}, err
	}
	return s.Cursor(ctx, agentID)
}

// Cursor returns the highest stored agent row id per kind.
func (s *Service) Cursor(ctx context.Context, agentID int64) (api.Cursor, error) {
	var c api.Cursor
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COALESCE(MAX(remote_id), 0) FROM backup_runs WHERE agent_id = ?1),
		(SELECT COALESCE(MAX(remote_id), 0) FROM check_results WHERE agent_id = ?1),
		(SELECT COALESCE(MAX(remote_id), 0) FROM metrics WHERE agent_id = ?1)`, agentID).
		Scan(&c.BackupRunID, &c.CheckID, &c.MetricID)
	return c, err
}

// AgentStatus is everything the UI shows about one agent's current state.
type AgentStatus struct {
	Agent      Agent
	Backups    []store.BackupRun // latest per (job, repository)
	Alerts     []store.Alert
	Checks     []store.CheckResult // latest per check
	Metrics    *store.Metrics      // latest sample, nil if none
	HasMetrics bool
}

func (s *Service) Status(ctx context.Context, agentID int64) (AgentStatus, error) {
	var st AgentStatus
	var err error
	if st.Agent, err = s.GetAgent(ctx, agentID); err != nil {
		return st, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT remote_id, job, repository, started_at, finished_at, success,
		snapshot_id, bytes_added, total_bytes, error FROM backup_runs b
		WHERE agent_id = ?1 AND remote_id = (SELECT MAX(remote_id) FROM backup_runs
			WHERE agent_id = ?1 AND job = b.job AND repository = b.repository)
		ORDER BY job, repository`, agentID)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var b store.BackupRun
		var started, finished int64
		if err := rows.Scan(&b.ID, &b.Job, &b.Repository, &started, &finished, &b.Success,
			&b.SnapshotID, &b.BytesAdded, &b.TotalBytes, &b.Error); err != nil {
			rows.Close()
			return st, err
		}
		b.StartedAt, b.FinishedAt = time.Unix(started, 0), time.Unix(finished, 0)
		st.Backups = append(st.Backups, b)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT key, since, message FROM agent_alerts WHERE agent_id = ? ORDER BY since`, agentID)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var a store.Alert
		var since int64
		if err := rows.Scan(&a.Key, &since, &a.Message); err != nil {
			rows.Close()
			return st, err
		}
		a.Firing, a.Since = true, time.Unix(since, 0)
		st.Alerts = append(st.Alerts, a)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT remote_id, name, at, ok, latency_ms, error FROM check_results c
		WHERE agent_id = ?1 AND remote_id = (SELECT MAX(remote_id) FROM check_results WHERE agent_id = ?1 AND name = c.name)
		ORDER BY name`, agentID)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var c store.CheckResult
		var at, ms int64
		if err := rows.Scan(&c.ID, &c.Name, &at, &c.OK, &ms, &c.Error); err != nil {
			rows.Close()
			return st, err
		}
		c.At, c.Latency = time.Unix(at, 0), time.Duration(ms)*time.Millisecond
		st.Checks = append(st.Checks, c)
	}
	rows.Close()

	var m store.Metrics
	var at int64
	err = s.db.QueryRowContext(ctx, `SELECT at, cpu, memory, swap, load1, disk_max FROM metrics
		WHERE agent_id = ? ORDER BY at DESC LIMIT 1`, agentID).Scan(&at, &m.CPU, &m.Memory, &m.Swap, &m.Load1, &m.DiskMax)
	if err == nil {
		m.At = time.Unix(at, 0)
		st.Metrics = &m
	}
	return st, nil
}

// MetricSeries returns samples since a time, oldest first, for charts.
func (s *Service) MetricSeries(ctx context.Context, agentID int64, since time.Time) ([]store.Metrics, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, cpu, memory, swap, load1, disk_max FROM metrics
		WHERE agent_id = ? AND at >= ? ORDER BY at`, agentID, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Metrics
	for rows.Next() {
		var m store.Metrics
		var at int64
		if err := rows.Scan(&at, &m.CPU, &m.Memory, &m.Swap, &m.Load1, &m.DiskMax); err != nil {
			return nil, err
		}
		m.At = time.Unix(at, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// BackupHistory returns an agent's most recent backup runs, newest first.
func (s *Service) BackupHistory(ctx context.Context, agentID int64, limit int) ([]store.BackupRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT remote_id, job, repository, started_at, finished_at, success,
		snapshot_id, bytes_added, total_bytes, error FROM backup_runs WHERE agent_id = ?
		ORDER BY finished_at DESC, remote_id DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.BackupRun
	for rows.Next() {
		var b store.BackupRun
		var started, finished int64
		if err := rows.Scan(&b.ID, &b.Job, &b.Repository, &started, &finished, &b.Success,
			&b.SnapshotID, &b.BytesAdded, &b.TotalBytes, &b.Error); err != nil {
			return nil, err
		}
		b.StartedAt, b.FinishedAt = time.Unix(started, 0), time.Unix(finished, 0)
		out = append(out, b)
	}
	return out, rows.Err()
}

// Prune deletes check results and metrics older than before.
func (s *Service) Prune(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM check_results WHERE at < ?`, before.Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM metrics WHERE at < ?`, before.Unix())
	return err
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
