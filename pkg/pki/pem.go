// Package pki is the certificates of the control plane: a CA that signs the certificates of agents, the
// server's own certificate, the TLS settings both ends use, and the pinning an agent uses once, to
// join. It uses the standard library only (ECDSA P-256 everywhere) and keeps no state of its own.
package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
)

// maxPEMSize caps what ParseCSR and the loaders read: a certificate or a request is a few KiB.
const maxPEMSize = 64 << 10

// PEM block types.
const (
	typeCert = "CERTIFICATE"
	typeKey  = "PRIVATE KEY"
	typeCSR  = "CERTIFICATE REQUEST"
)

func newKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

// newSerial is a random 128-bit serial number, never zero.
func newSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	for {
		serial, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return nil, fmt.Errorf("random serial: %w", err)
		}
		if serial.Sign() > 0 {
			return serial, nil
		}
	}
}

func encodeCert(der []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: typeCert, Bytes: der}) }

// EncodeKey is the PEM (PKCS #8) of a private key.
func EncodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: typeKey, Bytes: der}), nil
}

// ParseKey reads the PEM of a P-256 private key.
func ParseKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, err := singleBlock(keyPEM, typeKey)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("private key: want an ECDSA P-256 key")
	}
	return key, nil
}

// ParseCert reads the PEM of one certificate.
func ParseCert(certPEM []byte) (*x509.Certificate, error) {
	block, err := singleBlock(certPEM, typeCert)
	if err != nil {
		return nil, fmt.Errorf("certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("certificate: %w", err)
	}
	return cert, nil
}

// singleBlock decodes data that holds exactly one PEM block of the given type and nothing after it.
func singleBlock(data []byte, blockType string) (*pem.Block, error) {
	if len(data) > maxPEMSize {
		return nil, errors.New("too large")
	}
	block, rest := pem.Decode(data)
	switch {
	case block == nil:
		return nil, errors.New("no PEM data")
	case block.Type != blockType:
		return nil, fmt.Errorf("got a %q block, want %q", block.Type, blockType)
	case len(bytes.TrimSpace(rest)) > 0:
		return nil, errors.New("more than one PEM block")
	}
	return block, nil
}

// WritePrivate writes a secret (a private key) to path, readable by its owner only. It writes a
// temporary file in the same directory and renames it, so a crash leaves the old file or the new one,
// never half of one, and the key is never on disk with wider permissions. Missing directories are
// created with mode 0700.
func WritePrivate(path string, data []byte) error { return writeAtomic(path, data, 0o600) }

// WritePublic is WritePrivate for what anyone may read (a certificate), mode 0644.
func WritePublic(path string, data []byte) error { return writeAtomic(path, data, 0o644) }

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	file, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	tmp := file.Name()
	cleanup := func(err error) error {
		_ = file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Chmod(perm); err != nil {
		return cleanup(err)
	}
	if _, err := file.Write(data); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readPrivate reads a private key file and refuses one that others can read or write.
func readPrivate(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users: run chmod 600 %s", path, path)
	}
	return os.ReadFile(path)
}
