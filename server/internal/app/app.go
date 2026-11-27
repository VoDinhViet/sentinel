// Package app is the control plane as a service: the one place that opens the database and the CA and
// puts the parts together. The commands and the HTTP handlers call it and do nothing else.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"sentinel/pkg/pki"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/auth"
	"sentinel/server/internal/config"
	"sentinel/server/internal/enroll"
	"sentinel/server/internal/store"
)

// ErrNotInitialized means the state directory has no CA yet.
var ErrNotInitialized = errors.New("not initialized: run sentinel-server init")

// ErrAlreadyInitialized means init would overwrite a CA, keys or a database that exist.
var ErrAlreadyInitialized = errors.New("already initialized")

// App is an opened control plane.
type App struct {
	Config *config.Config
	Store  *store.Store
	CA     *pki.CA
	Auth   *auth.Service
	Enroll *enroll.Service
}

// Open opens an initialized control plane.
func Open(ctx context.Context, cfg *config.Config) (*App, error) {
	files := cfg.Files()
	if _, err := os.Stat(files.CACert()); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	ca, err := pki.LoadCA(files.CACert(), files.CAKey())
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, files.DB())
	if err != nil {
		return nil, err
	}
	return &App{Config: cfg, Store: st, CA: ca, Auth: auth.New(st), Enroll: enroll.New(st, ca)}, nil
}

func (a *App) Close() error { return a.Store.Close() }

// InitResult is what an administrator needs after init.
type InitResult struct {
	Fingerprint string // of the CA: what `sentinel agent join` is given to trust this server
}

// Init makes a new control plane in the state directory: the CA, the server's certificate, the database
// and the administrator's password. It refuses when any of those files exists, and removes what it
// made if it fails half way, so it can be run again.
func Init(ctx context.Context, cfg *config.Config, password string) (result InitResult, err error) {
	if err := auth.ValidatePassword(password); err != nil {
		return InitResult{}, err
	}
	files := cfg.Files()
	for _, path := range files.All() {
		if _, err := os.Stat(path); err == nil {
			return InitResult{}, fmt.Errorf("%w: %s exists", ErrAlreadyInitialized, path)
		}
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return InitResult{}, fmt.Errorf("create %s: %w", cfg.StateDir, err)
	}
	defer func() {
		if err != nil {
			for _, path := range files.All() {
				_ = os.Remove(path)
				_ = os.Remove(path + "-wal")
				_ = os.Remove(path + "-shm")
			}
		}
	}()

	now := time.Now()
	ca, err := pki.NewCA(now)
	if err != nil {
		return InitResult{}, err
	}
	caKey, err := ca.KeyPEM()
	if err != nil {
		return InitResult{}, err
	}
	chain, serverKey, err := ca.IssueServer(cfg.Hosts, now)
	if err != nil {
		return InitResult{}, err
	}
	st, err := store.Open(ctx, files.DB())
	if err != nil {
		return InitResult{}, err
	}
	defer st.Close()
	if err := auth.New(st).SetPassword(ctx, password); err != nil {
		return InitResult{}, err
	}
	if err := st.InTx(ctx, func(tx *sql.Tx) error {
		return audit.Append(ctx, tx, now, audit.Entry{Action: "server.init", Detail: "CA " + ca.Fingerprint()})
	}); err != nil {
		return InitResult{}, err
	}
	for _, file := range []struct {
		path string
		data []byte
		keep bool // a secret
	}{{files.CAKey(), caKey, true}, {files.ServerKey(), serverKey, true}, {files.ServerCert(), chain, false}, {files.CACert(), ca.CertPEM(), false}} {
		write := pki.WritePublic
		if file.keep {
			write = pki.WritePrivate
		}
		if err := write(file.path, file.data); err != nil {
			return InitResult{}, err
		}
	}
	return InitResult{Fingerprint: ca.Fingerprint()}, nil
}
