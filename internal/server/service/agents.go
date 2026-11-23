// Package service holds the sentinel server's business logic and storage.
// It knows nothing about HTTP so the HTML UI, the agent API and any future
// JSON API share the same rules.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"sentinel/internal/api"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrConflict     = errors.New("already exists")
)

type Service struct {
	db  *sql.DB
	Now func() time.Time

	mu      sync.Mutex
	wakeups map[int64]chan struct{} // per agent: signalled when a command is queued
}

func Open(path string) (*Service, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, Now: time.Now, wakeups: map[int64]chan struct{}{}}, nil
}

func (s *Service) Close() error { return s.db.Close() }

func (s *Service) now() time.Time { return s.Now() }

type Agent struct {
	ID            int64
	Name          string
	CreatedAt     time.Time
	LastSeen      time.Time // zero if never connected
	Hostname      string
	Version       string
	Jobs          []api.Job
	Checks        []string
	RunningConfig int64
	ConfigStatus  api.ConfigStatus
	// BeszelSystem is the id of the linked Beszel system ("" = none, match by name).
	BeszelSystem string
	// BeszelOwned is set when Sentinel created that system, so removing the
	// agent may remove it too.
	BeszelOwned bool
}

// Online reports whether the agent has reported within offlineAfter.
func (a Agent) Online(now time.Time, offlineAfter time.Duration) bool {
	return !a.LastSeen.IsZero() && now.Sub(a.LastSeen) < offlineAfter
}

func newToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "snt_" + hex.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

var agentName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// CreateAgent registers an agent and returns its token. The token is only
// stored hashed, so this is the one chance to show it.
func (s *Service) CreateAgent(ctx context.Context, name string) (Agent, string, error) {
	if !agentName.MatchString(name) {
		return Agent{}, "", Invalid("invalid name %q: use letters, digits, '.', '_' or '-'", name)
	}
	token := newToken()
	res, err := s.db.ExecContext(ctx, `INSERT INTO agents (name, token_hash, created_at) VALUES (?, ?, ?)`,
		name, hashToken(token), s.now().Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return Agent{}, "", fmt.Errorf("agent %q: %w", name, ErrConflict)
		}
		return Agent{}, "", err
	}
	id, _ := res.LastInsertId()
	a, err := s.GetAgent(ctx, id)
	return a, token, err
}

// RotateToken issues a new token; the old one stops working immediately.
func (s *Service) RotateToken(ctx context.Context, id int64) (string, error) {
	token := newToken()
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET token_hash = ? WHERE id = ?`, hashToken(token), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return token, nil
}

func (s *Service) DeleteAgent(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Authenticate returns the agent owning token.
func (s *Service) Authenticate(ctx context.Context, token string) (Agent, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM agents WHERE token_hash = ?`, hashToken(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrUnauthorized
	}
	if err != nil {
		return Agent{}, err
	}
	return s.GetAgent(ctx, id)
}

const agentCols = `id, name, created_at, last_seen, hostname, version, jobs_json, checks_json,
	running_config, config_status_version, config_status_error, beszel_system, beszel_owned`

func scanAgent(row interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	var created, seen int64
	var jobs, checks, statusErr string
	err := row.Scan(&a.ID, &a.Name, &created, &seen, &a.Hostname, &a.Version, &jobs, &checks,
		&a.RunningConfig, &a.ConfigStatus.Version, &statusErr, &a.BeszelSystem, &a.BeszelOwned)
	if err != nil {
		return a, err
	}
	a.CreatedAt = time.Unix(created, 0)
	if seen > 0 {
		a.LastSeen = time.Unix(seen, 0)
	}
	// The columns were written by Ingest; a bad value means a damaged row, not a
	// reason to fail the whole listing.
	if err := json.Unmarshal([]byte(jobs), &a.Jobs); err != nil {
		slog.Warn("agent has unreadable jobs", "agent", a.Name, "error", err)
	}
	if err := json.Unmarshal([]byte(checks), &a.Checks); err != nil {
		slog.Warn("agent has unreadable checks", "agent", a.Name, "error", err)
	}
	a.ConfigStatus.Error = statusErr
	a.ConfigStatus.Applied = a.ConfigStatus.Version > 0 && statusErr == ""
	return a, nil
}

func (s *Service) GetAgent(ctx context.Context, id int64) (Agent, error) {
	a, err := scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Service) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetBeszelSystem links an agent to a Beszel system id ("" removes the link).
// owned records that Sentinel created the system.
func (s *Service) SetBeszelSystem(ctx context.Context, id int64, system string, owned bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET beszel_system = ?, beszel_owned = ? WHERE id = ?`, system, owned && system != "", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && regexp.MustCompile(`UNIQUE constraint failed`).MatchString(err.Error())
}
