package pki

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"
)

// ServerTLSConfig is the TLS of the control plane: TLS 1.3, its own certificate, and a client
// certificate checked against the CA when one is shown. A client that shows none is let in, because
// joining needs no certificate; the routes that need an agent require a verified one themselves.
func ServerTLSConfig(cert tls.Certificate, ca *x509.CertPool) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    ca,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}
}

// CtlTLSConfig is the TLS of the listener for sentinelctl: TLS 1.3 and the server's own
// certificate. No client certificate is asked for; you sign in with a password.
func CtlTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
}

// ClientTLSConfig is the TLS an agent (or sentinelctl) uses once it has the CA: TLS 1.3, the server
// checked against ca, and cert shown to the server when it is not nil.
func ClientTLSConfig(ca *x509.CertPool, cert *tls.Certificate) *tls.Config {
	config := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: ca}
	if cert != nil {
		config.Certificates = []tls.Certificate{*cert}
	}
	return config
}

// PinnedTLSConfig is for the one connection that has no CA yet: joining. It trusts the CA whose
// fingerprint the operator gave (see ParseFingerprint) and nothing else. The server shows its
// certificate followed by its CA; the CA must have that fingerprint, and the server's certificate must
// be signed by it, for host (the host name or address of the URL), for server use, and be valid now.
//
// This is the only place that sets InsecureSkipVerify. It does so to replace the system roots
// check with VerifyConnection, which is stricter: it does all of that check, against the pinned CA.
// host is passed in because a client that dials an address does not record a name in the connection,
// and a check against an empty name would pass for any certificate.
func PinnedTLSConfig(fingerprint, host string) (*tls.Config, error) {
	want, err := ParseFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	if host == "" {
		return nil, errors.New("pinned TLS: no host to check the certificate against")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		ServerName:         host,
		InsecureSkipVerify: true, //nolint:gosec // replaced by VerifyConnection, which checks the chain against the pinned CA
		VerifyConnection: func(state tls.ConnectionState) error {
			return verifyPinned(state.PeerCertificates, want, host, time.Now())
		},
	}, nil
}

// verifyPinned is the check PinnedTLSConfig makes of the certificates a server showed.
func verifyPinned(chain []*x509.Certificate, wantFingerprint, host string, now time.Time) error {
	if len(chain) < 2 {
		return errors.New("the server did not send its CA: cannot check it against the fingerprint")
	}
	leaf := chain[0]
	roots := x509.NewCertPool()
	var pinned *x509.Certificate
	for _, candidate := range chain[1:] {
		if candidate.IsCA && sameFingerprint(candidate, wantFingerprint) {
			pinned = candidate
			roots.AddCert(candidate)
			break
		}
	}
	if pinned == nil {
		return fingerprintError(Fingerprint(chain[len(chain)-1]), wantFingerprint)
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		Roots:       roots,
		DNSName:     host,
		CurrentTime: now,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}
