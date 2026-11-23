package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Audit outcomes.
const (
	OutcomeOK     = "ok"
	OutcomeFailed = "failed"
	OutcomeDenied = "denied"
)

// AuditEntry is one row of the append-only audit log.
type AuditEntry struct {
	ID        int64
	At        time.Time
	UserID    int64
	Username  string
	IP        string
	Action    string
	AgentID   int64
	AgentName string
	Job       string
	Details   map[string]any // never put secrets here
	Outcome   string
}

// Audit appends an entry. When e.Username is empty the actor in ctx is used.
func (s *Service) Audit(ctx context.Context, e AuditEntry) error {
	if e.Username == "" {
		a := ActorFrom(ctx)
		e.UserID, e.Username = a.UserID, a.Username
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeOK
	}
	details := []byte("{}")
	if len(e.Details) > 0 {
		details, _ = json.Marshal(e.Details)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log (at, user_id, username, ip, action, agent_id, agent_name, job, details, outcome)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.now().Unix(), e.UserID, e.Username, e.IP, e.Action, e.AgentID, e.AgentName, e.Job, string(details), e.Outcome)
	return err
}

// AuditFilter narrows ListAudit. Zero fields match everything.
type AuditFilter struct {
	Username string
	Action   string // exact, or a prefix ending in "." such as "config."
	AgentID  int64
	Outcome  string
	BeforeID int64 // only entries older than this id (for paging)
	Limit    int
}

// ListAudit returns entries newest first.
func (s *Service) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	var where []string
	var args []any
	if f.Username != "" {
		where, args = append(where, "username = ? COLLATE NOCASE"), append(args, f.Username)
	}
	if strings.HasSuffix(f.Action, ".") {
		where, args = append(where, "action LIKE ? ESCAPE '\\'"), append(args, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Action)+"%")
	} else if f.Action != "" {
		where, args = append(where, "action = ?"), append(args, f.Action)
	}
	if f.AgentID != 0 {
		where, args = append(where, "agent_id = ?"), append(args, f.AgentID)
	}
	if f.Outcome != "" {
		where, args = append(where, "outcome = ?"), append(args, f.Outcome)
	}
	if f.BeforeID != 0 {
		where, args = append(where, "id < ?"), append(args, f.BeforeID)
	}
	query := `SELECT id, at, user_id, username, ip, action, agent_id, agent_name, job, details, outcome FROM audit_log`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY id DESC LIMIT ?", append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		var details string
		if err := rows.Scan(&e.ID, &at, &e.UserID, &e.Username, &e.IP, &e.Action, &e.AgentID, &e.AgentName, &e.Job, &details, &e.Outcome); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0)
		json.Unmarshal([]byte(details), &e.Details)
		out = append(out, e)
	}
	return out, rows.Err()
}
