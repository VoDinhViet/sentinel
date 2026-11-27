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
	"sync/atomic"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/pki"
)

// The files of a renewal that has not been put in place yet. A renewal writes the new key and certificate
// under these names first, so that at no moment is there only half of a pair (see LoadIdentity).
const (
	nextCertFile = "cert.next.pem"
	nextKeyFile  = "key.next.pem"
)

// Client calls the control plane with the certificate of this agent. The certificate it shows is read
// when a connection is made, so a renewed one is used from the next request on, with no restart.
type Client struct {
	where address
	dir   string
	http  *http.Client
	cert  atomic.Pointer[tls.Certificate]
}

// NewClient reads the identity in dir and returns a client of the control plane at rawURL.
func NewClient(dir, rawURL string) (*Client, error) {
	where, err := parseAddress(rawURL)
	if err != nil {
		return nil, err
	}
	identity, err := LoadIdentity(dir)
	if err != nil {
		return nil, err
	}
	client := &Client{where: where, dir: dir}
	client.cert.Store(identity.Cert)
	config := pki.ClientTLSConfig(identity.CA, nil)
	config.ServerName = where.host
	config.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return client.cert.Load(), nil }
	client.http = newHTTPClient(config)
	return client, nil
}

// Identity is what the agent proves itself with: its certificate and key, and the CA it trusts.
type Identity struct {
	Cert *tls.Certificate
	CA   *x509.CertPool
}

// LoadIdentity reads the identity from dir. A renewal that was cut short (power lost between two
// renames) leaves the new certificate or the new key under another name; the pair that matches is used,
// the newest first, so the agent is never locked out of a control plane by a renewal that did not finish.
func LoadIdentity(dir string) (Identity, error) {
	caPEM, err := os.ReadFile(filepath.Join(dir, CAFile))
	if err != nil {
		return Identity{}, fmt.Errorf("the agent's CA in %s: %w", dir, err)
	}
	ca, err := pki.ParseCert(caPEM)
	if err != nil {
		return Identity{}, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	var firstErr error
	for _, certFile := range []string{nextCertFile, CertFile} {
		for _, keyFile := range []string{KeyFile, nextKeyFile} {
			pair, err := tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
			if err == nil {
				return Identity{Cert: &pair, CA: pool}, nil
			}
			if firstErr == nil && certFile == CertFile && keyFile == KeyFile {
				firstErr = err
			}
		}
	}
	return Identity{}, fmt.Errorf("the agent's certificate in %s: %w", dir, firstErr)
}

// NotAfter is when the certificate in use ends.
func (c *Client) NotAfter() time.Time {
	pair := c.cert.Load()
	if pair == nil || len(pair.Certificate) == 0 {
		return time.Time{}
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return time.Time{}
	}
	return leaf.NotAfter
}

// WhoAmI asks the control plane whom it takes this agent for.
func (c *Client) WhoAmI(ctx context.Context) (agentapi.WhoAmI, error) {
	var who agentapi.WhoAmI
	err := call(ctx, c.http, http.MethodGet, c.where.base+agentapi.RouteWhoAmI, nil, &who)
	return who, err
}

// Report tells the control plane how this agent is. The answer says when to report again, and its clock.
func (c *Client) Report(ctx context.Context, report agentapi.Report) (agentapi.ReportResponse, error) {
	var answer agentapi.ReportResponse
	err := call(ctx, c.http, http.MethodPost, c.where.base+agentapi.RouteReport, report, &answer)
	return answer, err
}

// Renew asks for a new certificate for a new key, and puts both in place. The old certificate stays valid
// until it ends, so a renewal that fails half way loses nothing. The files are written so that, whenever
// the process stops, LoadIdentity finds a key and certificate that belong together.
func (c *Client) Renew(ctx context.Context) (time.Time, error) {
	keyPEM, csrPEM, err := pki.NewKeyAndCSR()
	if err != nil {
		return time.Time{}, err
	}
	// The new key is written first, before the control plane is asked for anything: a directory that
	// cannot be written (read-only, full) fails here, and no certificate is issued that could not be kept.
	if err := pki.WritePrivate(filepath.Join(c.dir, nextKeyFile), keyPEM); err != nil {
		return time.Time{}, fmt.Errorf("cannot write the new key: %w", err)
	}
	renewed, err := c.renewWith(ctx, keyPEM, csrPEM)
	if err != nil {
		_ = os.Remove(filepath.Join(c.dir, nextKeyFile))
	}
	return renewed, err
}

// renewWith asks for the certificate for csrPEM, checks the answer and puts it in place.
func (c *Client) renewWith(ctx context.Context, keyPEM, csrPEM []byte) (time.Time, error) {
	var answer agentapi.RenewResponse
	if err := call(ctx, c.http, http.MethodPost, c.where.base+agentapi.RouteRenew, agentapi.RenewRequest{CSR: string(csrPEM)}, &answer); err != nil {
		return time.Time{}, err
	}
	pair, err := tls.X509KeyPair([]byte(answer.Cert), keyPEM) // the certificate is for the key made here
	if err != nil {
		return time.Time{}, fmt.Errorf("the renewed certificate is not for the new key: %w", err)
	}
	if err := c.verifyRenewed(pair); err != nil {
		return time.Time{}, err
	}
	if err := installRenewal(c.dir, []byte(answer.Cert)); err != nil {
		return time.Time{}, err
	}
	c.cert.Store(&pair)
	return answer.NotAfter, nil
}

// Reload reads the identity in the directory again. The agent was joined again (sentinel agent join --force)
// while the daemon runs: the next request is made with the new certificate, without a restart.
func (c *Client) Reload() error {
	identity, err := LoadIdentity(c.dir)
	if err != nil {
		return err
	}
	c.cert.Store(identity.Cert)
	return nil
}

// Ping tells the control plane the agent is alive, without a report: for a round in which the report could
// not be built (the database was busy), so the agent does not look offline while its daemon runs.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.WhoAmI(ctx)
	return err
}

// verifyRenewed checks that the certificate is for this agent's name, signed by the CA it trusts.
func (c *Client) verifyRenewed(pair tls.Certificate) error {
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	oldName, err := currentName(c.cert.Load())
	if err != nil {
		return err
	}
	name, err := pki.AgentName(leaf)
	if err != nil || name != oldName {
		return fmt.Errorf("the renewed certificate is for %q, not %q", name, oldName)
	}
	identity, err := LoadIdentity(c.dir)
	if err != nil {
		return err
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: identity.CA, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("the renewed certificate is not signed by the CA this agent trusts: %w", err)
	}
	return nil
}

func currentName(pair *tls.Certificate) (string, error) {
	if pair == nil || len(pair.Certificate) == 0 {
		return "", errors.New("no certificate in use")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return "", err
	}
	return pki.AgentName(leaf)
}

// installRenewal puts the new certificate and the key written before (key.next.pem) in place. Each step leaves
// a pair LoadIdentity can use: the certificate is written whole under its own name, then the key and the
// certificate are renamed over the old ones. Between the two renames the key is new and the old certificate
// does not match it, but cert.next.pem and the new key do.
func installRenewal(dir string, certPEM []byte) error {
	if err := pki.WritePublic(filepath.Join(dir, nextCertFile), certPEM); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, nextKeyFile), filepath.Join(dir, KeyFile)); err != nil {
		return fmt.Errorf("put the new key in place: %w", err)
	}
	if err := os.Rename(filepath.Join(dir, nextCertFile), filepath.Join(dir, CertFile)); err != nil {
		return fmt.Errorf("put the new certificate in place: %w", err)
	}
	return nil
}
