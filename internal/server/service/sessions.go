package service

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CreateSession starts an admin session and returns its cookie token and
// CSRF token.
func (s *Service) CreateSession(ctx context.Context, ttl time.Duration) (token, csrf string, err error) {
	token, csrf = newToken(), newToken()
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now.Unix()); err != nil {
		return "", "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, csrf, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), csrf, now.Add(ttl).Unix())
	return token, csrf, err
}

// Session returns the CSRF token of a live session.
func (s *Service) Session(ctx context.Context, token string) (csrf string, err error) {
	var expires int64
	err = s.db.QueryRowContext(ctx, `SELECT csrf, expires_at FROM sessions WHERE token_hash = ?`, hashToken(token)).
		Scan(&csrf, &expires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && s.now().Unix() >= expires) {
		return "", ErrUnauthorized
	}
	return csrf, err
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}
