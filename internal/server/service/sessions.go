package service

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CreateSession starts a session for actor and returns its cookie token and
// CSRF token.
func (s *Service) CreateSession(ctx context.Context, actor Actor, ttl time.Duration) (token, csrf string, err error) {
	token, csrf = newToken(), newToken()
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now.Unix()); err != nil {
		return "", "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, csrf, expires_at, user_id, username) VALUES (?, ?, ?, ?, ?)`,
		hashToken(token), csrf, now.Add(ttl).Unix(), actor.UserID, actor.Username)
	return token, csrf, err
}

// Session returns the CSRF token and the user of a live session. A session of
// a database user is only live while that user exists and is enabled, and it
// carries the user's current role, so changes apply immediately.
// Sessions of the bootstrap admin (user 0) are always admin.
func (s *Service) Session(ctx context.Context, token string) (csrf string, actor Actor, err error) {
	var expires int64
	var role, name sql.NullString
	var disabled sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT s.csrf, s.expires_at, s.user_id, s.username, u.username, u.role, u.disabled
		FROM sessions s LEFT JOIN users u ON u.id = s.user_id AND s.user_id > 0
		WHERE s.token_hash = ?`, hashToken(token)).
		Scan(&csrf, &expires, &actor.UserID, &actor.Username, &name, &role, &disabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && s.now().Unix() >= expires) {
		return "", Actor{}, ErrUnauthorized
	}
	if err != nil {
		return "", Actor{}, err
	}
	if actor.UserID == 0 {
		actor.Role = RoleAdmin
		return csrf, actor, nil
	}
	if !role.Valid || disabled.Int64 != 0 {
		return "", Actor{}, ErrUnauthorized
	}
	actor.Username, actor.Role = name.String, role.String
	return csrf, actor, nil
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}
