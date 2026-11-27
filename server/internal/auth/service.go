package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"sentinel/server/internal/audit"
	"sentinel/server/internal/store"
)

// Limits of a session and of guessing.
const (
	SessionLifetime = 12 * time.Hour // from sign-in, however much it is used
	SessionIdle     = time.Hour      // without a request
	// touchEvery is how rarely a request writes "last used": a busy client should not write on every call.
	touchEvery = time.Minute

	// MaxFailures is how many wrong passwords in a row from one address block it.
	MaxFailures = 5
	// UnderAttackFailures is how many a single address gets once the whole server has seen
	// ServerFailureCap wrong passwords in the last FailureWindow.
	UnderAttackFailures = 1
	ServerFailureCap    = 30
	BlockFor            = 15 * time.Minute
	// FailureWindow is how long a wrong password is remembered: the count of an address starts again
	// when its last one is older, and the server-wide count looks back this far.
	FailureWindow = time.Hour

	// hashSlots is how many passwords are hashed at once: one hash is a fraction of a second of one CPU.
	hashSlots = 2
)

var (
	// ErrNotInitialized means no administrator password was ever set (run `sentinel-server init`).
	ErrNotInitialized = errors.New("no administrator password is set: run sentinel-server init")
	// ErrBadPassword is the answer to a wrong password, whatever the reason; it says no more than that.
	ErrBadPassword = errors.New("wrong password")
	// ErrUnauthorized means the session token is unknown, ended or expired.
	ErrUnauthorized = errors.New("not signed in")
)

// BlockedError says an address is blocked from signing in until a time.
type BlockedError struct{ Until time.Time }

func (e *BlockedError) Error() string {
	return fmt.Sprintf("too many wrong passwords from this address: try again after %s", e.Until.Format(time.TimeOnly))
}

// Session is a sign-in that has not ended.
type Session struct {
	ID        string // the first 8 characters of the hash of the token: enough to tell sessions apart in the audit log
	IP        string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Service holds the administrator's password and sessions.
type Service struct {
	store *store.Store
	slots chan struct{}
}

func New(st *store.Store) *Service {
	return &Service{store: st, slots: make(chan struct{}, hashSlots)}
}

// SetPassword stores a new administrator password (the first one, or a replacement) and ends every
// session. It is for the person at the machine (`sentinel-server init` and `passwd`), who needs no old
// password: they can already read the database.
func (s *Service) SetPassword(ctx context.Context, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx *sql.Tx) error { return s.replacePassword(ctx, tx, hash, time.Now(), "password.set") })
}

func (s *Service) replacePassword(ctx context.Context, tx *sql.Tx, hash string, now time.Time, action string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin (id, password_hash, changed_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET password_hash = excluded.password_hash, changed_at = excluded.changed_at`, hash, now.Unix()); err != nil {
		return fmt.Errorf("store password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return fmt.Errorf("end sessions: %w", err)
	}
	return audit.Append(ctx, tx, now, audit.Entry{Action: action})
}

// Initialized says a password was set.
func (s *Service) Initialized(ctx context.Context) (bool, error) {
	var count int
	if err := s.store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin`).Scan(&count); err != nil {
		return false, fmt.Errorf("read admin: %w", err)
	}
	return count > 0, nil
}

// Login checks the password of an address and starts a session. The token it returns is shown once and
// is not kept: only its hash is. Wrong answers are ErrBadPassword (or *BlockedError once the address is
// blocked) and reveal nothing else.
func (s *Service) Login(ctx context.Context, ip, password string) (token string, session Session, err error) {
	changedAt, err := s.verify(ctx, ip, password)
	if err != nil {
		return "", Session{}, err
	}
	token, tokenHash, err := newToken()
	if err != nil {
		return "", Session{}, err
	}
	now := time.Now()
	session = Session{ID: tokenHash[:8], IP: ip, CreatedAt: now, ExpiresAt: now.Add(SessionLifetime)}
	err = s.store.InTx(ctx, func(tx *sql.Tx) error {
		// The password may have been replaced while it was being hashed: that session would outlive it.
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT changed_at FROM admin WHERE id = 1`).Scan(&current); err != nil || current != changedAt {
			return ErrBadPassword
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (token_hash, ip, created_at, last_used_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			tokenHash, ip, now.Unix(), now.Unix(), session.ExpiresAt.Unix()); err != nil {
			return fmt.Errorf("start session: %w", err)
		}
		return audit.Append(audit.WithActor(ctx, audit.Actor{Name: "admin", Session: session.ID, IP: ip}), tx, now, audit.Entry{Action: "login.ok"})
	})
	if err != nil {
		return "", Session{}, err
	}
	return token, session, nil
}

// verify is the part of signing in that is shared with changing the password: the address is not
// blocked, the password is right, and a wrong one is counted. It returns when the password it checked
// was set.
//
// The attempt is counted before the password is hashed, not after: hashing takes a moment, and
// attempts that arrive together would otherwise all be let in before the first failure is written.
func (s *Service) verify(ctx context.Context, ip, password string) (changedAt int64, err error) {
	var stored string
	switch err := s.store.DB.QueryRowContext(ctx, `SELECT password_hash, changed_at FROM admin WHERE id = 1`).Scan(&stored, &changedAt); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, ErrNotInitialized
	case err != nil:
		return 0, fmt.Errorf("read admin: %w", err)
	}
	attempt, err := s.reserve(ctx, ip)
	if err != nil {
		return 0, err
	}
	match, stale, err := s.check(ctx, stored, password)
	if err != nil {
		return 0, err
	}
	if !match {
		return 0, s.recordFailure(ctx, ip, attempt)
	}
	if stale {
		s.rehash(ctx, stored, password)
	}
	if _, err := s.store.DB.ExecContext(context.WithoutCancel(ctx), `DELETE FROM login_failures WHERE ip = ?`, ip); err != nil {
		return 0, fmt.Errorf("clear failures: %w", err)
	}
	return changedAt, nil
}

// check runs CheckPassword, at most hashSlots at a time.
func (s *Service) check(ctx context.Context, stored, password string) (match, stale bool, err error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return false, false, ctx.Err()
	}
	return CheckPassword(stored, password)
}

func (s *Service) hash(ctx context.Context, password string) (string, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return HashPassword(password)
}

// rehash replaces a hash made with fewer rounds, unless the password was replaced meanwhile. It is best
// effort: the sign-in has succeeded.
func (s *Service) rehash(ctx context.Context, stored, password string) {
	hash, err := s.hash(ctx, password)
	if err != nil {
		return
	}
	_, _ = s.store.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE admin SET password_hash = ? WHERE id = 1 AND password_hash = ?`, hash, stored)
}

// attempt is one try at a password that was let in by reserve.
type attempt struct {
	failures int  // tries in a row from the address, this one included
	last     bool // this was the last one the address gets: a wrong answer blocks it
}

// reserve lets one try in for an address, or refuses it with a *BlockedError. Looking at the block and
// counting the try is one transaction, so an address cannot get more than its tries however many
// requests it sends at once. A right password clears the count afterwards.
func (s *Service) reserve(ctx context.Context, ip string) (attempt, error) {
	now := time.Now()
	var try attempt
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		var failures int
		var lastAt, blockedUntil int64
		err := tx.QueryRowContext(ctx, `SELECT failures, last_at, blocked_until FROM login_failures WHERE ip = ?`, ip).Scan(&failures, &lastAt, &blockedUntil)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read failures: %w", err)
		}
		if blockedUntil > now.Unix() {
			return &BlockedError{Until: time.Unix(blockedUntil, 0)}
		}
		if now.Sub(time.Unix(lastAt, 0)) > FailureWindow || blockedUntil != 0 {
			failures = 0 // the old ones are forgotten
		}
		failures++
		allowed, err := s.allowedFailures(ctx, tx, now)
		if err != nil {
			return err
		}
		blockedUntil = 0
		if failures >= allowed {
			blockedUntil = now.Add(BlockFor).Unix()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO login_failures (ip, failures, last_at, blocked_until) VALUES (?, ?, ?, ?)
			ON CONFLICT(ip) DO UPDATE SET failures = excluded.failures, last_at = excluded.last_at, blocked_until = excluded.blocked_until`,
			ip, failures, now.Unix(), blockedUntil); err != nil {
			return fmt.Errorf("count try: %w", err)
		}
		try = attempt{failures: failures, last: blockedUntil != 0}
		if try.last {
			actor := audit.WithActor(ctx, audit.Actor{Name: "unknown", IP: ip})
			return audit.Append(actor, tx, now, audit.Entry{Action: "login.block", Detail: "until " + time.Unix(blockedUntil, 0).UTC().Format(time.RFC3339)})
		}
		return nil
	})
	return try, err
}

// recordFailure audits a wrong password (its count was made by reserve) and returns the error to answer
// with: the block when it was the address's last try.
func (s *Service) recordFailure(ctx context.Context, ip string, try attempt) error {
	now := time.Now()
	err := s.store.InTx(ctx, func(tx *sql.Tx) error {
		actor := audit.WithActor(ctx, audit.Actor{Name: "unknown", IP: ip})
		return audit.Append(actor, tx, now, audit.Entry{Action: "login.fail", Detail: "in a row: " + strconv.Itoa(try.failures)})
	})
	if err != nil {
		return err
	}
	if try.last {
		return &BlockedError{Until: now.Add(BlockFor)}
	}
	return ErrBadPassword
}

// allowedFailures is how many wrong passwords in a row an address may make. When the whole server has
// seen ServerFailureCap in the last FailureWindow, somebody is guessing from many addresses: each one then
// gets UnderAttackFailures. Nobody is locked out for it: an address that has not failed can still sign in.
func (s *Service) allowedFailures(ctx context.Context, tx *sql.Tx, now time.Time) (int, error) {
	var recent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'login.fail' AND at > ?`,
		now.Add(-FailureWindow).UnixMilli()).Scan(&recent); err != nil {
		return 0, fmt.Errorf("count failures: %w", err)
	}
	if recent >= ServerFailureCap {
		return UnderAttackFailures, nil
	}
	return MaxFailures, nil
}

// Authenticate finds the session of a token and counts the request as use. A session that is past its
// lifetime or has been idle too long is deleted and refused.
func (s *Service) Authenticate(ctx context.Context, token string) (Session, error) {
	now := time.Now()
	tokenHash := hashToken(token)
	var ip string
	var createdAt, lastUsedAt, expiresAt int64
	err := s.store.DB.QueryRowContext(ctx, `SELECT ip, created_at, last_used_at, expires_at FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&ip, &createdAt, &lastUsedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session: %w", err)
	}
	if now.Unix() >= expiresAt || now.Sub(time.Unix(lastUsedAt, 0)) >= SessionIdle {
		_, _ = s.store.DB.ExecContext(context.WithoutCancel(ctx), `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
		return Session{}, ErrUnauthorized
	}
	if now.Sub(time.Unix(lastUsedAt, 0)) >= touchEvery {
		if _, err := s.store.DB.ExecContext(ctx, `UPDATE sessions SET last_used_at = ? WHERE token_hash = ?`, now.Unix(), tokenHash); err != nil {
			return Session{}, fmt.Errorf("touch session: %w", err)
		}
	}
	return Session{ID: tokenHash[:8], IP: ip, CreatedAt: time.Unix(createdAt, 0), ExpiresAt: time.Unix(expiresAt, 0)}, nil
}

// Logout ends the session of a token. A token that is not a session is not an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	tokenHash := hashToken(token)
	return s.store.InTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
		if err != nil {
			return fmt.Errorf("end session: %w", err)
		}
		if ended, _ := result.RowsAffected(); ended == 0 {
			return nil
		}
		return audit.Append(audit.WithActor(ctx, audit.Actor{Name: "admin", Session: tokenHash[:8]}), tx, time.Now(), audit.Entry{Action: "logout"})
	})
}

// ChangePassword replaces the password, after checking the old one the way a sign-in does (so it cannot
// be used to guess it), and ends every session, this one too: the caller signs in again.
func (s *Service) ChangePassword(ctx context.Context, ip, oldPassword, newPassword string) error {
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	if _, err := s.verify(ctx, ip, oldPassword); err != nil {
		return err
	}
	hash, err := s.hash(ctx, newPassword)
	if err != nil {
		return err
	}
	actor := audit.WithActor(ctx, audit.Actor{Name: "admin", IP: ip})
	return s.store.InTx(ctx, func(tx *sql.Tx) error { return s.replacePassword(actor, tx, hash, time.Now(), "password.change") })
}

// Prune deletes the sessions that have ended and the failure counts that no longer matter.
func (s *Service) Prune(ctx context.Context) error {
	now := time.Now()
	if _, err := s.store.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR last_used_at <= ?`,
		now.Unix(), now.Add(-SessionIdle).Unix()); err != nil {
		return fmt.Errorf("prune sessions: %w", err)
	}
	if _, err := s.store.DB.ExecContext(ctx, `DELETE FROM login_failures WHERE blocked_until <= ? AND last_at <= ?`,
		now.Unix(), now.Add(-FailureWindow).Unix()); err != nil {
		return fmt.Errorf("prune failures: %w", err)
	}
	return nil
}

// newToken is a random 256-bit token and the hash that is stored for it.
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
