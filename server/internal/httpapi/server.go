// Package httpapi is the HTTP side of the control plane: two sets of routes, one for the agents (mutual
// TLS, on `listen`) and one for sentinelctl (a session, on `ctl_listen`). The handlers only turn
// requests into calls of package app and answers into JSON; the work is done there.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/ratelimit"
	"sentinel/server/internal/app"
	"sentinel/server/internal/ctlapi"
)

// Request bodies are small, and the largest are capped here.
const (
	maxJoinBody = 16 << 10 // a token and a certificate request
	maxCtlBody  = 4 << 10  // a password, a name
)

// Server answers the requests of agents and of sentinelctl.
type Server struct {
	app *app.App
	// joinLimit allows an address a few joins a minute (a token is 256 bits, so this is about the
	// work of parsing a request, not about guessing).
	joinLimit *ratelimit.Limiter
	// ctlLimit keeps one address from asking for hundreds of things a second; the password
	// attempts are limited separately, by package auth.
	ctlLimit *ratelimit.Limiter
}

func New(a *app.App) *Server {
	return &Server{app: a, joinLimit: ratelimit.New(0.2, 10), ctlLimit: ratelimit.New(20, 40)}
}

// AgentHandler is the routes of `listen`. Joining needs no certificate; every other route needs the
// certificate of an agent that is still accepted.
func (s *Server) AgentHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+agentapi.RouteJoin, s.join)
	mux.Handle("GET "+agentapi.RouteWhoAmI, s.agentOnly(s.whoAmI))
	return recovered(logged(mux))
}

// CtlHandler is the routes of `ctl_listen`. Everything but login needs a session.
func (s *Server) CtlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST "+ctlapi.RouteLogin, s.limited(s.login))
	mux.Handle("POST "+ctlapi.RouteLogout, s.signedIn(s.logout))
	mux.Handle("POST "+ctlapi.RoutePasswd, s.signedIn(s.passwd))
	mux.Handle("GET "+ctlapi.RouteAgents, s.signedIn(s.listAgents))
	mux.Handle("POST "+ctlapi.RouteAgents, s.signedIn(s.addAgent))
	mux.Handle("DELETE "+ctlapi.RouteAgent, s.signedIn(s.revokeAgent))
	mux.Handle("GET "+ctlapi.RouteAudit, s.signedIn(s.listAudit))
	mux.Handle("GET "+ctlapi.RouteAuditVerify, s.signedIn(s.verifyAudit))
	return recovered(logged(mux))
}

// shutdownWait is how long Serve lets running requests finish once its context is done.
const shutdownWait = 5 * time.Second

// Serve answers on ln (already TLS) until ctx is done, then shuts down gracefully. The caller
// listens, so a port that is taken is found at startup and not in the background.
func Serve(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
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
		slog.Warn("shutdown", "err", err)
	}
	return nil
}
