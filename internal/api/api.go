// Package api is the HTTP API of the daemon: the status, the jobs, their snapshots, and
// starting or stopping a backup. It only turns requests into calls of the service and the
// answers into JSON; the work is done in package app.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"sentinel/internal/app"
	"sentinel/internal/doctor"
	"sentinel/internal/restic"
	"sentinel/internal/status"
)

// Backend is what the API asks of the service; *app.Service is one.
type Backend interface {
	Status(ctx context.Context, now time.Time) (status.Report, error)
	JobRepositories() []app.JobRepository
	ListSnapshots(ctx context.Context, jobName, repo string) ([]restic.Snapshot, error)
	StartBackup(jobName, repo string) (pid int, logPath string, err error)
	RequestStop(jobName string) error
	Doctor(ctx context.Context) []doctor.Finding
}

var _ Backend = (*app.Service)(nil)

// Server answers the API requests of one backend. Every /api/ request needs the bearer token.
type Server struct {
	backend Backend
	token   string
}

// New returns the server for a backend. token must not be empty.
func New(backend Backend, token string) *Server {
	return &Server{backend: backend, token: token}
}

// Handler is the routes of the API. /healthz is open so a probe needs no secret.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.Handle("GET /api/v1/status", s.authorized(s.status))
	mux.Handle("GET /api/v1/alerts", s.authorized(s.alerts))
	mux.Handle("GET /api/v1/jobs", s.authorized(s.jobs))
	mux.Handle("GET /api/v1/jobs/{job}/snapshots", s.authorized(s.snapshots))
	mux.Handle("POST /api/v1/jobs/{job}/backups", s.authorized(s.startBackup))
	mux.Handle("DELETE /api/v1/jobs/{job}/backups/running", s.authorized(s.stopBackup))
	mux.Handle("GET /api/v1/doctor", s.authorized(s.doctor))
	return logged(mux)
}

// shutdownWait is how long Serve lets running requests finish once its context is done.
const shutdownWait = 5 * time.Second

// Serve answers on ln with handler until ctx is done, then shuts down gracefully. The caller
// listens, so a port that is taken is found at startup and not in the background.
func Serve(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute, // a snapshot listing waits for restic
		IdleTimeout:       time.Minute,
	}
	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWait)
	defer cancel()
	if err := srv.Shutdown(stopCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Warn("api shutdown", "err", err)
	}
	return nil
}
