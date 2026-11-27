// Package auth is how the administrator signs in to the control plane: one password, no user name, and
// a session after it. The password is stored as a PBKDF2 hash; a session token and the password never
// are. An address that guesses wrong too often is blocked for a while.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Parameters of the password hash. The rounds are stored in the hash, so raising them later does not
// lock anyone out: an older hash is accepted and replaced at the next sign-in.
const (
	hashScheme = "pbkdf2-sha256"
	// Rounds is the PBKDF2 iteration count of a new hash (the OWASP figure for HMAC-SHA256).
	Rounds     = 600_000
	maxRounds  = 10_000_000 // what a stored hash may ask for: a damaged value must not make a sign-in take minutes
	saltLength = 16
	keyLength  = 32

	MinPasswordLength = 12
	maxPasswordLength = 1024 // PBKDF2 hashes the whole password: bound the work one request can ask for
)

// rounds is what a new hash uses; tests lower it so they do not spend seconds in PBKDF2.
var rounds = Rounds

// ValidatePassword says whether a password may be chosen.
func ValidatePassword(password string) error {
	switch length := utf8.RuneCountInString(password); {
	case length < MinPasswordLength:
		return fmt.Errorf("the password must have at least %d characters", MinPasswordLength)
	case len(password) > maxPasswordLength:
		return fmt.Errorf("the password must have at most %d bytes", maxPasswordLength)
	}
	return nil
}

// HashPassword is the stored form of a password: pbkdf2-sha256$<rounds>$<salt>$<hash>.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("random salt: %w", err)
	}
	return hashWith(password, rounds, salt)
}

func hashWith(password string, count int, salt []byte) (string, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, count, keyLength)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	encoding := base64.RawStdEncoding
	return strings.Join([]string{hashScheme, strconv.Itoa(count), encoding.EncodeToString(salt), encoding.EncodeToString(key)}, "$"), nil
}

// CheckPassword compares a password with a stored hash in constant time. stale says the hash was made
// with fewer rounds than Rounds, so the caller should store a new one now that it has the password.
// An error means the stored hash is damaged, not that the password is wrong.
func CheckPassword(stored, password string) (match, stale bool, err error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false, false, errors.New("the stored password hash is not in the pbkdf2-sha256 form")
	}
	count, err := strconv.Atoi(parts[1])
	if err != nil || count < 1 || count > maxRounds {
		return false, false, errors.New("the stored password hash has an unusable round count")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 8 {
		return false, false, errors.New("the stored password hash has an unusable salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) != keyLength {
		return false, false, errors.New("the stored password hash has an unusable key")
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, count, keyLength)
	if err != nil {
		return false, false, fmt.Errorf("hash password: %w", err)
	}
	return subtle.ConstantTimeCompare(got, want) == 1, count < rounds, nil
}
