package pki

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const fingerprintPrefix = "sha256:"

// Fingerprint is the SHA-256 of a certificate as "sha256:<64 hex digits>".
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return fingerprintPrefix + hex.EncodeToString(sum[:])
}

// ParseFingerprint reads a fingerprint as an operator types or pastes it: with or without "sha256:",
// in either case, with or without colons between the bytes. It returns the canonical form.
func ParseFingerprint(text string) (string, error) {
	digits := strings.ToLower(strings.TrimSpace(text))
	digits = strings.TrimPrefix(digits, fingerprintPrefix)
	digits = strings.ReplaceAll(digits, ":", "")
	raw, err := hex.DecodeString(digits)
	if err != nil || len(raw) != sha256.Size {
		return "", errors.New("fingerprint: want sha256: and 64 hex digits")
	}
	return fingerprintPrefix + hex.EncodeToString(raw), nil
}

// sameFingerprint compares a certificate with a canonical fingerprint.
func sameFingerprint(cert *x509.Certificate, want string) bool {
	return Fingerprint(cert) == want
}

func fingerprintError(got, want string) error {
	return fmt.Errorf("the server's CA is %s, not the %s that was asked for", got, want)
}
