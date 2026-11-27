// Package app is the control plane as a service: the one place that opens the database and the CA and
// puts the parts together. The commands and the HTTP handlers call it and do nothing else.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"sentinel/pkg/alert"
	"sentinel/pkg/notify"
	"sentinel/pkg/pki"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/auth"
	"sentinel/server/internal/config"
	"sentinel/server/internal/enroll"
	"sentinel/server/internal/inventory"
	"sentinel/server/internal/scheduler"
	"sentinel/server/internal/store"
	"sentinel/server/internal/task"
	"sentinel/server/internal/watchdog"
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
	// Inventory holds what the agents say about themselves.
	Inventory *inventory.Service
	// Tasks is the queue of what the agents are asked to do.
	Tasks *task.Service
	// Scheduler queues tasks by timetable and keeps the maintenance windows.
	Scheduler *scheduler.Service
	// Alerts raises and closes the control plane's own alerts; AlertStore reads them.
	Alerts     *alert.Manager
	AlertStore watchdog.AlertStore
	Watchdog   *watchdog.Watchdog
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
	return newApp(ctx, cfg, st, ca)
}

// newApp puts the parts together over an open database.
func newApp(ctx context.Context, cfg *config.Config, st *store.Store, ca *pki.CA) (*App, error) {
	alertStore := watchdog.AlertStore{DB: st.DB}
	alerts := &alert.Manager{Notifier: notify.NewMulti(cfg.Notifiers), Store: alertStore, Server: "sentinel-server", Cooldown: cfg.Cooldown}
	if err := alerts.Load(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("load alert state: %w", err)
	}
	enrolled := enroll.New(st, ca)
	inv := inventory.New(st)
	tasks := task.New(st)
	timetable := scheduler.New(st, tasks)
	return &App{
		Config: cfg, Store: st, CA: ca, Auth: auth.New(st), Enroll: enrolled, Inventory: inv, Tasks: tasks, Scheduler: timetable,
		Alerts: alerts, AlertStore: alertStore,
		Watchdog: &watchdog.Watchdog{Alerts: alerts, Agents: enrolled, Inventory: inv, Maintenance: timetable, OfflineAfter: cfg.OfflineAfter, Started: time.Now()},
	}, nil
}

func (a *App) Close() error { return a.Store.Close() }

// InitResult is what you need after init.
type InitResult struct {
	Fingerprint string // of the CA: what `sentinel agent join` is given to trust this server
}

// Init makes a new control plane in the state directory: the CA, the server's certificate, the database
// and the password. It refuses when any of those files exists, and removes what it
// made if it fails half way, so it can be run again.
func Init(ctx context.Context, cfg *config.Config, password string) (result InitResult, err error) {
	if err := auth.ValidatePassword(password); err != nil {
		return InitResult{}, err
	}
	if len(cfg.Hosts) == 0 {
		return InitResult{}, errors.New("no host name or address for the server certificate")
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
	chain, serverKey, err := ca.IssueServer(certHosts(cfg.Hosts), now)
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

// certHosts are the names the server's certificate is made for: the hosts agents use, and the loopback
// names, because ctl_listen is on this machine by default and sentinelctl reaches it through an SSH tunnel.
func certHosts(hosts []string) []string {
	all := slices.Clone(hosts)
	for _, loopback := range []string{"localhost", "127.0.0.1", "::1"} {
		if !slices.Contains(all, loopback) {
			all = append(all, loopback)
		}
	}
	return all
}
