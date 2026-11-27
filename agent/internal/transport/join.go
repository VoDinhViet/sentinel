package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/pki"
)

// JoinOptions says which control plane to join, and where to keep the identity it gives.
type JoinOptions struct {
	URL           string // https://host:port of `listen`
	Token         string // the join token
	CAFingerprint string // of the control plane's CA, as sentinel-server init printed it
	Dir           string // where the key and certificates go
	Force         bool   // replace an identity that is already there
}

// Joined is what a successful join gives back.
type Joined struct {
	Name     string // the name the control plane gave this agent, which is in its certificate
	NotAfter time.Time
}

// Join makes a key, asks the control plane to certify it for a join token, and writes the key, the
// certificate and the CA to Dir. The control plane is trusted only if its CA has the fingerprint given
// (that is what takes the place of a CA file copied by hand), and what it answers is checked again here:
// the CA is that one, the certificate is signed by it, is for the name it says, and is for the key that
// never left this machine.
func Join(ctx context.Context, opts JoinOptions) (Joined, error) {
	where, err := parseAddress(opts.URL)
	if err != nil {
		return Joined{}, err
	}
	if opts.Token == "" {
		return Joined{}, errors.New("no join token")
	}
	want, err := pki.ParseFingerprint(opts.CAFingerprint)
	if err != nil {
		return Joined{}, err
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, KeyFile)); err == nil && !opts.Force {
		return Joined{}, fmt.Errorf("%s already holds an identity: this server has joined before (use --force to replace it, with a new join token)", opts.Dir)
	}
	keyPEM, csrPEM, err := pki.NewKeyAndCSR()
	if err != nil {
		return Joined{}, err
	}
	pinned, err := pki.PinnedTLSConfig(want, where.host)
	if err != nil {
		return Joined{}, err
	}
	var answer agentapi.JoinResponse
	err = call(ctx, newHTTPClient(pinned), http.MethodPost, where.base+agentapi.RouteJoin,
		agentapi.JoinRequest{Token: opts.Token, CSR: string(csrPEM)}, &answer)
	var refused *statusError
	if errors.As(err, &refused) && refused.status == http.StatusForbidden {
		return Joined{}, ErrJoinRefused
	}
	if err != nil {
		return Joined{}, err
	}
	notAfter, err := checkAnswer(answer, keyPEM, want)
	if err != nil {
		return Joined{}, fmt.Errorf("the control plane's answer is not usable: %w", err)
	}
	if err := writeIdentity(opts.Dir, answer, keyPEM); err != nil {
		return Joined{}, err
	}
	return Joined{Name: answer.Name, NotAfter: notAfter}, nil
}

// checkAnswer verifies a join answer against the key and the fingerprint, and returns when the
// certificate ends.
func checkAnswer(answer agentapi.JoinResponse, keyPEM []byte, wantCA string) (time.Time, error) {
	ca, err := pki.ParseCert([]byte(answer.CA))
	if err != nil {
		return time.Time{}, err
	}
	if got := pki.Fingerprint(ca); got != wantCA {
		return time.Time{}, fmt.Errorf("the CA in the answer is %s, not %s", got, wantCA)
	}
	cert, err := pki.ParseCert([]byte(answer.Cert))
	if err != nil {
		return time.Time{}, err
	}
	name, err := pki.AgentName(cert)
	if err != nil {
		return time.Time{}, err
	}
	if name != answer.Name {
		return time.Time{}, fmt.Errorf("the certificate is for %q, not %q", name, answer.Name)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return time.Time{}, fmt.Errorf("the certificate is not signed by that CA for an agent: %w", err)
	}
	if _, err := tls.X509KeyPair([]byte(answer.Cert), keyPEM); err != nil { // the key is the one that made the request
		return time.Time{}, fmt.Errorf("the certificate is not for this agent's key: %w", err)
	}
	return cert.NotAfter, nil
}

// writeIdentity writes the CA, the key and last the certificate: a join that stops half way has no
// certificate, so it does not look joined.
func writeIdentity(dir string, answer agentapi.JoinResponse, keyPEM []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := pki.WritePublic(filepath.Join(dir, CAFile), []byte(answer.CA)); err != nil {
		return err
	}
	if err := pki.WritePrivate(filepath.Join(dir, KeyFile), keyPEM); err != nil {
		return err
	}
	return pki.WritePublic(filepath.Join(dir, CertFile), []byte(answer.Cert))
}
