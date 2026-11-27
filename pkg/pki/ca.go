package pki

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Lifetimes. An agent certificate is short so a stolen one stops working by itself, and long enough
// that an agent that is off for a month can still come back without joining again.
const (
	CAValidity     = 10 * 365 * 24 * time.Hour
	ServerValidity = 365 * 24 * time.Hour
	AgentValidity  = 90 * 24 * time.Hour
	// RenewBefore is how long before a certificate ends its holder asks for a new one.
	RenewBefore = 30 * 24 * time.Hour
	// clockSkew is how far back a new certificate starts, so a machine whose clock is a little slow accepts it.
	clockSkew = 5 * time.Minute
)

// CA signs the certificates of the control plane. It holds a private key: keep it where only the
// server can read it.
type CA struct {
	Cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// NewCA makes a new self-signed CA valid for CAValidity from now.
func NewCA(now time.Time) (*CA, error) {
	key, err := newKey()
	if err != nil {
		return nil, fmt.Errorf("new CA key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Sentinel CA", Organization: []string{"Sentinel"}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0, // it signs end certificates only
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("new CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("new CA certificate: %w", err)
	}
	return &CA{Cert: cert, key: key}, nil
}

// ParseCA builds a CA from its PEM files and checks that the key belongs to the certificate.
func ParseCA(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := ParseCert(certPEM)
	if err != nil {
		return nil, fmt.Errorf("CA: %w", err)
	}
	key, err := ParseKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("CA: %w", err)
	}
	if !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("CA: the certificate is not a CA certificate")
	}
	public, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !public.Equal(&key.PublicKey) {
		return nil, errors.New("CA: the key does not belong to the certificate")
	}
	return &CA{Cert: cert, key: key}, nil
}

// LoadCA reads a CA from its two files. It refuses a key file that other users can read.
func LoadCA(certPath, keyPath string) (*CA, error) {
	keyPEM, err := readPrivate(keyPath)
	if err != nil {
		return nil, fmt.Errorf("CA: %w", err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("CA: read %s: %w", certPath, err)
	}
	return ParseCA(certPEM, keyPEM)
}

// CertPEM is the certificate of the CA, which is public.
func (ca *CA) CertPEM() []byte { return encodeCert(ca.Cert.Raw) }

// KeyPEM is the private key of the CA. Write it with WritePrivate.
func (ca *CA) KeyPEM() ([]byte, error) { return EncodeKey(ca.key) }

// Fingerprint is what an operator gives `sentinel agent join` so the agent knows it talks to this CA.
func (ca *CA) Fingerprint() string { return Fingerprint(ca.Cert) }

// Pool is a certificate pool that trusts this CA only.
func (ca *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// usable says the CA can sign at time now.
func (ca *CA) usable(now time.Time) error {
	if now.Before(ca.Cert.NotBefore) || now.After(ca.Cert.NotAfter) {
		return errors.New("the CA certificate is not valid now")
	}
	return nil
}

// IssueServer makes a certificate and key for the server, for the host names and IP addresses in
// hosts (what agents and sentinelctl put in the URL). The certificate is returned as a chain, the
// server's own first and the CA after it: an agent that joins knows only the fingerprint of the CA and
// takes the CA itself from that chain.
func (ca *CA) IssueServer(hosts []string, now time.Time) (chainPEM, keyPEM []byte, err error) {
	if len(hosts) == 0 {
		return nil, nil, errors.New("server certificate: no host name or address")
	}
	if err := ca.usable(now); err != nil {
		return nil, nil, err
	}
	key, err := newKey()
	if err != nil {
		return nil, nil, fmt.Errorf("server key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Sentinel server"},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(ServerValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, host := range hosts {
		if address := net.ParseIP(host); address != nil {
			template.IPAddresses = append(template.IPAddresses, address)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("server certificate: %w", err)
	}
	keyPEM, err = EncodeKey(key)
	if err != nil {
		return nil, nil, err
	}
	return append(encodeCert(der), ca.CertPEM()...), keyPEM, nil
}
