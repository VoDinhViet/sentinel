// Package inventory is what the control plane knows about its agents from what they say: when each was
// last heard from, which version it runs, and its latest report.
package inventory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/termtext"
	"sentinel/server/internal/store"
)

// Status is how an agent stands, in the words of sentinelctl.
type Status string

const (
	Pending Status = "PENDING" // named, has not joined yet
	Online  Status = "ONLINE"
	Offline Status = "OFFLINE"
	Revoked Status = "REVOKED"
)

// StatusOf says how an agent stands at now. An active agent is offline when it has not been heard from for
// offlineAfter; one that joined and was never heard from counts from when it joined.
func StatusOf(state string, enrolledAt, lastSeen, now time.Time, offlineAfter time.Duration) Status {
	switch state {
	case "pending":
		return Pending
	case "revoked":
		return Revoked
	}
	heard := lastSeen
	if heard.IsZero() {
		heard = enrolledAt
	}
	if now.Sub(heard) > offlineAfter {
		return Offline
	}
	return Online
}

// Service records what agents say.
type Service struct{ store *store.Store }

func New(st *store.Store) *Service { return &Service{store: st} }

// Seen records that the agent made a request at now.
func (s *Service) Seen(ctx context.Context, name string, now time.Time) error {
	if _, err := s.store.DB.ExecContext(ctx, `UPDATE agents SET last_seen = ? WHERE name = ?`, now.Unix(), name); err != nil {
		return fmt.Errorf("record last seen: %w", err)
	}
	return nil
}

// SaveReport keeps report as the agent's latest, and the version it carries as the agent's version. The
// report is cleaned first: it came from another machine.
func (s *Service) SaveReport(ctx context.Context, name string, report agentapi.Report, now time.Time) error {
	report = report.Clean()
	body, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_reports (agent, received_at, report) VALUES (?, ?, ?)
			ON CONFLICT (agent) DO UPDATE SET received_at = excluded.received_at, report = excluded.report`,
			name, now.Unix(), string(body)); err != nil {
			return fmt.Errorf("save report: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET version = ?, last_seen = ? WHERE name = ?`, report.Version, now.Unix(), name); err != nil {
			return fmt.Errorf("save version: %w", err)
		}
		return nil
	})
}

// ErrNoReport means the agent has not sent a report.
var ErrNoReport = errors.New("the agent has not sent a report yet")

// Report returns the latest report of the agent and when it arrived. What was stored is cleaned again, so
// a row written by an older version of the control plane cannot put a control character on a screen.
func (s *Service) Report(ctx context.Context, name string) (agentapi.Report, time.Time, error) {
	var body string
	var receivedAt int64
	err := s.store.DB.QueryRowContext(ctx, `SELECT report, received_at FROM agent_reports WHERE agent = ?`, name).Scan(&body, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return agentapi.Report{}, time.Time{}, ErrNoReport
	}
	if err != nil {
		return agentapi.Report{}, time.Time{}, fmt.Errorf("read report: %w", err)
	}
	var report agentapi.Report
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		return agentapi.Report{}, time.Time{}, fmt.Errorf("the stored report of %s is damaged: %w", termtext.Clean(name), err)
	}
	return report.Clean(), time.Unix(receivedAt, 0), nil
}

// HasReported says whether the agent has ever sent a report. A request without one (the whoami that `sentinel
// agent join` makes) shows the agent is there, but not that its daemon runs.
func (s *Service) HasReported(ctx context.Context, name string) (bool, error) {
	var found int
	err := s.store.DB.QueryRowContext(ctx, `SELECT 1 FROM agent_reports WHERE agent = ?`, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
