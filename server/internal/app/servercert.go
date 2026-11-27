package app

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"sentinel/pkg/pki"
	"sentinel/server/internal/audit"
)

// ServerCert loads the certificate the server speaks with. It makes a new one first when the one on disk
// ends within pki.RenewBefore, is missing or damaged, or does not cover a name in `hosts` (the
// a name was added to `hosts` and the service restarted). The CA stays the same, so no agent has to do anything.
func (a *App) ServerCert(ctx context.Context, now time.Time) (tls.Certificate, error) {
	files := a.Config.Files()
	hosts := certHosts(a.Config.Hosts)
	current, err := tls.LoadX509KeyPair(files.ServerCert(), files.ServerKey())
	if err == nil && serverCertFits(current, hosts, now) {
		return current, nil
	}
	chain, key, err := a.CA.IssueServer(hosts, now)
	if err != nil {
		return tls.Certificate{}, err
	}
	// The key first: a stop in between leaves a pair that does not match, which the next start replaces.
	if err := pki.WritePrivate(files.ServerKey(), key); err != nil {
		return tls.Certificate{}, err
	}
	if err := pki.WritePublic(files.ServerCert(), chain); err != nil {
		return tls.Certificate{}, err
	}
	renewed, err := tls.LoadX509KeyPair(files.ServerCert(), files.ServerKey())
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("the new server certificate: %w", err)
	}
	err = a.Store.InTx(ctx, func(tx *sql.Tx) error {
		return audit.Append(ctx, tx, now, audit.Entry{Action: "server.cert", Detail: "until " + renewed.Leaf.NotAfter.UTC().Format(time.RFC3339)})
	})
	return renewed, err
}

func serverCertFits(cert tls.Certificate, hosts []string, now time.Time) bool {
	leaf := cert.Leaf
	if leaf == nil || leaf.NotAfter.Sub(now) < pki.RenewBefore || now.Before(leaf.NotBefore) {
		return false
	}
	for _, host := range hosts {
		if leaf.VerifyHostname(host) != nil {
			return false
		}
	}
	return true
}

// ServerCertSource is the certificate the listeners present, which can be replaced while they run: a
// server that is never restarted must still get a new certificate before the old one ends.
type ServerCertSource struct {
	app     *App
	current atomic.Pointer[tls.Certificate]
}

// NewServerCertSource loads (and if need be renews) the certificate.
func (a *App) NewServerCertSource(ctx context.Context, now time.Time) (*ServerCertSource, error) {
	source := &ServerCertSource{app: a}
	if err := source.Refresh(ctx, now); err != nil {
		return nil, err
	}
	return source, nil
}

// Get is tls.Config.GetCertificate: every new connection gets the current certificate.
func (s *ServerCertSource) Get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return s.current.Load(), nil
}

// Refresh renews the certificate when it is near its end, and starts presenting the new one.
func (s *ServerCertSource) Refresh(ctx context.Context, now time.Time) error {
	cert, err := s.app.ServerCert(ctx, now)
	if err != nil {
		return err
	}
	s.current.Store(&cert)
	return nil
}

// Use makes config present the source's certificate instead of a fixed one.
func (s *ServerCertSource) Use(config *tls.Config) *tls.Config {
	config.Certificates = nil
	config.GetCertificate = s.Get
	return config
}
