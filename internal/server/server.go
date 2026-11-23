// Package server is the HTTP layer of `sentinel server`: the agent API, the
// admin JSON API and the embedded single-page web UI. Business rules live in package service.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sentinel/internal/beszel"
	"sentinel/internal/config"
	"sentinel/internal/server/service"
)

type Server struct {
	Config  *config.ServerConfig
	Service *service.Service
	// Version is shown in the UI footer.
	Version string

	// Beszel is nil unless the config has a beszel section.
	Beszel Monitor

	mux     *http.ServeMux
	live    chan struct{} // one slot per open live stream
	limiter *loginLimiter
}

func New(cfg *config.ServerConfig, svc *service.Service) *Server {
	s := &Server{Config: cfg, Service: svc, mux: http.NewServeMux(), live: make(chan struct{}, maxLive), limiter: newLoginLimiter(5, time.Minute)}
	if b := cfg.Beszel; b != nil {
		s.Beszel = beszel.New(beszel.Config{URL: b.URL, Email: b.Email, Password: b.Password, Poll: b.Poll, PublicURL: b.PublicURL})
	}
	s.routesAgent()
	s.routesAPI()
	s.routesUsers()
	s.routesSPA()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if s.Config.SecureCookies == nil || *s.Config.SecureCookies {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	s.mux.ServeHTTP(w, r)
}

// Scripts must come from this origin. Styles may be inline because the UI
// libraries (toasts, popovers) set style attributes and inject small sheets.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

type ctxKey int

const agentKey ctxKey = iota

// withAgent authenticates the Bearer token and stores the agent in the context.
func (s *Server) withAgent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			jsonError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		a, err := s.Service.Authenticate(r.Context(), token)
		if errors.Is(err, service.ErrUnauthorized) {
			jsonError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), agentKey, a)))
	}
}

func agentFrom(r *http.Request) service.Agent {
	return r.Context().Value(agentKey).(service.Agent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSON reads at most limit bytes of JSON into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		jsonError(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return false
	}
	return true
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	jsonError(w, http.StatusInternalServerError, "internal error")
}
