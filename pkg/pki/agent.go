package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The identity of an agent is a URI in the subject alternative names of its certificate:
// sentinel://agent/<name>. The name is put there by the server, never taken from the request.
const (
	agentScheme = "sentinel"
	agentHost   = "agent"
)

var agentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidName says name can be the name of an agent: letters, digits, dot, underscore and hyphen, at
// most 63 characters, starting with a letter or digit. It holds no ":" or "/", so it cannot change the
// URI it goes into, nor the alert keys built from it.
func ValidName(name string) bool { return agentName.MatchString(name) }

// Issued is a certificate the CA has signed.
type Issued struct {
	CertPEM  []byte
	Serial   string // lower-case hex; what the server keeps to revoke it
	NotAfter time.Time
}

// NewKeyAndCSR makes the key of an agent and a request for a certificate with that key. The request
// holds nothing but the public key: the server decides the name, so there is nothing to forge. The key
// is returned for the caller to write with WritePrivate; it never leaves the machine.
func NewKeyAndCSR() (keyPEM, csrPEM []byte, err error) {
	key, err := newKey()
	if err != nil {
		return nil, nil, fmt.Errorf("agent key: %w", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return nil, nil, fmt.Errorf("certificate request: %w", err)
	}
	keyPEM, err = EncodeKey(key)
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, pem.EncodeToMemory(&pem.Block{Type: typeCSR, Bytes: der}), nil
}

// ParseCSR reads a request and checks its signature (so the sender owns the key) and that the key is
// ECDSA P-256. Everything else in the request is ignored by SignAgent.
func ParseCSR(csrPEM []byte) (*ecdsa.PublicKey, error) {
	block, err := singleBlock(csrPEM, typeCSR)
	if err != nil {
		return nil, fmt.Errorf("certificate request: %w", err)
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("certificate request: %w", err)
	}
	if err := request.CheckSignature(); err != nil {
		return nil, fmt.Errorf("certificate request: %w", err)
	}
	public, ok := request.PublicKey.(*ecdsa.PublicKey)
	if !ok || public.Curve != elliptic.P256() {
		return nil, errors.New("certificate request: want an ECDSA P-256 key")
	}
	return public, nil
}

// SignAgent signs a certificate for the agent called name, for AgentValidity from now. The certificate
// is for client authentication only, is never a CA, and carries the name as its one URI. Whatever the
// request says about the subject, names, key usages or being a CA is ignored.
func (ca *CA) SignAgent(csrPEM []byte, name string, now time.Time) (Issued, error) {
	if !ValidName(name) {
		return Issued{}, fmt.Errorf("agent name %q is not valid", name)
	}
	if err := ca.usable(now); err != nil {
		return Issued{}, err
	}
	public, err := ParseCSR(csrPEM)
	if err != nil {
		return Issued{}, err
	}
	serial, err := newSerial()
	if err != nil {
		return Issued{}, err
	}
	notAfter := now.Add(AgentValidity)
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter // never outlive the CA
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		URIs:                  []*url.URL{{Scheme: agentScheme, Host: agentHost, Path: "/" + name}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, public, ca.key)
	if err != nil {
		return Issued{}, fmt.Errorf("agent certificate: %w", err)
	}
	// A certificate keeps whole seconds: report the time it holds, not the one that was asked for.
	return Issued{CertPEM: encodeCert(der), Serial: serial.Text(16), NotAfter: notAfter.Truncate(time.Second)}, nil
}

// SerialOf is the serial number of a certificate in the form Issued.Serial has.
func SerialOf(cert *x509.Certificate) string { return cert.SerialNumber.Text(16) }

// AgentName is the name an agent certificate stands for. It accepts only a certificate with exactly
// one URI, of the form sentinel://agent/<name>, so a certificate for anything else (the server's, one
// with several identities) is never taken for an agent.
func AgentName(cert *x509.Certificate) (string, error) {
	if len(cert.URIs) != 1 {
		return "", errors.New("certificate has no single agent identity")
	}
	uri := cert.URIs[0]
	name := strings.TrimPrefix(uri.Path, "/")
	if uri.Scheme != agentScheme || uri.Host != agentHost || uri.Path != "/"+name || !ValidName(name) {
		return "", errors.New("certificate identity is not sentinel://agent/<name>")
	}
	return name, nil
}

// ShouldRenew says the certificate ends within RenewBefore of now.
func ShouldRenew(cert *x509.Certificate, now time.Time) bool {
	return cert.NotAfter.Sub(now) <= RenewBefore
}
