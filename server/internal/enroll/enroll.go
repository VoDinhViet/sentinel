// Package enroll is how a server becomes an agent of the control plane: the administrator names it and
// gets a join token, the agent shows the token with a request for a certificate, and the CA signs it.
// It also revokes an agent and says whether the certificate on a request is still good.
package enroll

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"sentinel/pkg/pki"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/store"
)

// TokenTTL is how long a join token can be used.
const TokenTTL = 24 * time.Hour

var (
	// ErrExists means an agent with that name has already joined (or was revoked): a name is used once.
	ErrExists = errors.New("an agent with that name already exists")
	// ErrNotFound means there is no agent with that name.
	ErrNotFound = errors.New("no agent with that name")
	// ErrInvalidToken is the answer to a join token that is unknown, used, expired or of a revoked agent.
	// It does not say which, so a guess learns nothing.
	ErrInvalidToken = errors.New("the join token is not valid")
	// ErrBadRequest means the request for a certificate is no good (the token is not used up by it).
	ErrBadRequest = errors.New("bad certificate request")
	// ErrCertRefused means a certificate is not accepted: unknown, revoked, or of an agent that is not active.
	ErrCertRefused = errors.New("the certificate is not accepted")
)

// Service enrolls and revokes agents.
type Service struct {
	store *store.Store
	ca    *pki.CA
}

func New(st *store.Store, ca *pki.CA) *Service { return &Service{store: st, ca: ca} }

// Create adds the agent called name, waiting to join, and returns the token it joins with. The token is
// shown once; only its hash is kept. Asking again for an agent that has not joined gives a new token and
// voids the old one.
func (s *Service) Create(ctx context.Context, name string) (token string, expires time.Time, err error) {
	if !pki.ValidName(name) {
		return "", time.Time{}, fmt.Errorf("%q is not a valid agent name: use letters, digits, '.', '_' and '-', at most 63", name)
	}
	token, tokenHash, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now()
	expires = now.Add(TokenTTL)
	err = s.store.InTx(ctx, func(tx *sql.Tx) error {
		var state string
		switch err := tx.QueryRowContext(ctx, `SELECT state FROM agents WHERE name = ?`, name).Scan(&state); {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, `INSERT INTO agents (name, state, created_at) VALUES (?, 'pending', ?)`, name, now.Unix()); err != nil {
				return fmt.Errorf("add agent: %w", err)
			}
		case err != nil:
			return fmt.Errorf("read agent: %w", err)
		case state != "pending":
			return ErrExists
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM join_tokens WHERE agent = ? AND used_at IS NULL`, name); err != nil {
			return fmt.Errorf("void old tokens: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO join_tokens (token_hash, agent, created_at, expires_at) VALUES (?, ?, ?, ?)`,
			tokenHash, name, now.Unix(), expires.Unix()); err != nil {
			return fmt.Errorf("add token: %w", err)
		}
		return audit.Append(ctx, tx, now, audit.Entry{Action: "agent.enroll", Target: name})
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Joined is what an agent gets back for a good join token.
type Joined struct {
	Name     string
	CertPEM  []byte
	CAPEM    []byte
	NotAfter time.Time
}

// Join spends a join token: the CA signs the request for the agent the token was made for. The name in
// the certificate is the agent's, never one the request asks for. A request that is not a good request
// for a certificate fails without using up the token, so the agent can try again.
func (s *Service) Join(ctx context.Context, token string, csrPEM []byte) (Joined, error) {
	tokenHash := hashToken(token)
	now := time.Now()
	var joined Joined
	var refused bool
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		var name, state string
		var expiresAt int64
		var usedAt sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT t.agent, t.expires_at, t.used_at, a.state FROM join_tokens t JOIN agents a ON a.name = t.agent
			WHERE t.token_hash = ?`, tokenHash).Scan(&name, &expiresAt, &usedAt, &state)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (usedAt.Valid || now.Unix() >= expiresAt || state != "pending") {
			refused = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("read token: %w", err)
		}
		issued, err := s.ca.SignAgent(csrPEM, name, now)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE join_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`, now.Unix(), tokenHash); err != nil {
			return fmt.Errorf("use token: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO certs (serial, agent, issued_at, not_after) VALUES (?, ?, ?, ?)`,
			issued.Serial, name, now.Unix(), issued.NotAfter.Unix()); err != nil {
			return fmt.Errorf("store certificate: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'active', enrolled_at = ? WHERE name = ?`, now.Unix(), name); err != nil {
			return fmt.Errorf("activate agent: %w", err)
		}
		joined = Joined{Name: name, CertPEM: issued.CertPEM, CAPEM: s.ca.CertPEM(), NotAfter: issued.NotAfter}
		agent := audit.WithActor(ctx, audit.Actor{Name: "agent:" + name, IP: audit.ActorFrom(ctx).IP})
		return audit.Append(agent, tx, now, audit.Entry{Action: "agent.join", Target: name, Detail: "certificate " + issued.Serial})
	})
	if err != nil {
		return Joined{}, err
	}
	if refused {
		// Not in the audit log: anybody can send a wrong token, and the log never shrinks.
		slog.Warn("join refused", "ip", audit.ActorFrom(ctx).IP)
		return Joined{}, ErrInvalidToken
	}
	return joined, nil
}

// Revoke ends an agent: its certificates are refused from now on and its unused join tokens are void.
// Revoking an agent that is already revoked does nothing.
func (s *Service) Revoke(ctx context.Context, name string) error {
	now := time.Now()
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		var state string
		switch err := tx.QueryRowContext(ctx, `SELECT state FROM agents WHERE name = ?`, name).Scan(&state); {
		case errors.Is(err, sql.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("read agent: %w", err)
		case state == "revoked":
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET state = 'revoked' WHERE name = ?`, name); err != nil {
			return fmt.Errorf("revoke agent: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE certs SET revoked_at = ? WHERE agent = ? AND revoked_at IS NULL`, now.Unix(), name); err != nil {
			return fmt.Errorf("revoke certificates: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM join_tokens WHERE agent = ? AND used_at IS NULL`, name); err != nil {
			return fmt.Errorf("void tokens: %w", err)
		}
		return audit.Append(ctx, tx, now, audit.Entry{Action: "agent.revoke", Target: name})
	})
}

// Check says whether the certificate with this serial, shown as agent name, is accepted now: the
// control plane signed it, it is not revoked and has not ended, and its agent is active. It is called
// on every request of an agent; a certificate the TLS layer has already verified still has to pass it,
// because revoking is a row in the database, not a list the CA publishes.
func (s *Service) Check(ctx context.Context, serial, name string) error {
	var agent, state string
	var notAfter int64
	var revokedAt sql.NullInt64
	err := s.store.DB.QueryRowContext(ctx, `SELECT c.agent, c.not_after, c.revoked_at, a.state FROM certs c JOIN agents a ON a.name = c.agent
		WHERE c.serial = ?`, serial).Scan(&agent, &notAfter, &revokedAt, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCertRefused
	}
	if err != nil {
		return fmt.Errorf("read certificate: %w", err)
	}
	if agent != name || revokedAt.Valid || state != "active" || time.Now().Unix() >= notAfter {
		return ErrCertRefused
	}
	return nil
}

// newToken is a random 256-bit join token and the hash that is stored for it.
func newToken() (token, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("random token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
