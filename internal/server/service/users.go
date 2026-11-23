package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{2,31}$`)

const minPasswordLen = 10

type User struct {
	ID        int64
	Username  string
	Role      string
	Disabled  bool
	CreatedAt time.Time
}

func validatePassword(password string) error {
	if len(password) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	if len(password) > 72 { // bcrypt ignores the rest
		return errors.New("password must be at most 72 bytes")
	}
	return nil
}

// CreateUser adds a user with the given role.
func (s *Service) CreateUser(ctx context.Context, username, password, role string) (User, error) {
	if !usernameRe.MatchString(username) {
		return User{}, errors.New("invalid username: 3-32 characters, letters, digits, '.', '_' or '-'")
	}
	if !ValidRole(role) {
		return User{}, fmt.Errorf("invalid role %q", role)
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	now := s.now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, created_at) VALUES (?, ?, ?, ?)`,
		username, string(hash), role, now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, ErrConflict
		}
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, Role: role, CreatedAt: time.Unix(now.Unix(), 0)}, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, role, disabled, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var disabled, created int64
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &disabled, &created); err != nil {
			return nil, err
		}
		u.Disabled, u.CreatedAt = disabled != 0, time.Unix(created, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserUpdate changes the non-nil fields. Disabling a user or changing the
// password signs the user out everywhere.
type UserUpdate struct {
	Role     *string
	Disabled *bool
	Password *string
}

func (s *Service) UpdateUser(ctx context.Context, id int64, up UserUpdate) (User, error) {
	if up.Role != nil && !ValidRole(*up.Role) {
		return User{}, fmt.Errorf("invalid role %q", *up.Role)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, id).Scan(&exists); err != nil {
		return User{}, err
	}
	if exists == 0 {
		return User{}, ErrNotFound
	}
	if up.Role != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, *up.Role, id); err != nil {
			return User{}, err
		}
	}
	signOut := false
	if up.Disabled != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, boolInt(*up.Disabled), id); err != nil {
			return User{}, err
		}
		signOut = signOut || *up.Disabled
	}
	if up.Password != nil {
		if err := validatePassword(*up.Password); err != nil {
			return User{}, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*up.Password), bcrypt.DefaultCost)
		if err != nil {
			return User{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id); err != nil {
			return User{}, err
		}
		signOut = true
	}
	if signOut {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.getUser(ctx, id)
}

func (s *Service) getUser(ctx context.Context, id int64) (User, error) {
	users, err := s.ListUsers(ctx)
	if err != nil {
		return User{}, err
	}
	for _, u := range users {
		if u.ID == id {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

// dummyHash makes a lookup of an unknown user cost as much as a real one.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("sentinel-timing-dummy"), bcrypt.DefaultCost)

// VerifyUser checks a database user's credentials. Unknown, disabled and
// wrong-password cases all return ErrUnauthorized.
func (s *Service) VerifyUser(ctx context.Context, username, password string) (Actor, error) {
	var a Actor
	var hash string
	var disabled int64
	err := s.db.QueryRowContext(ctx, `SELECT id, username, role, password_hash, disabled FROM users WHERE username = ?`, username).
		Scan(&a.UserID, &a.Username, &a.Role, &hash, &disabled)
	if err != nil {
		hash = string(dummyHash)
	}
	passOK := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	if err != nil || !passOK || disabled != 0 {
		return Actor{}, ErrUnauthorized
	}
	return a, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
