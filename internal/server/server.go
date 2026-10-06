// Package server is the HTTP layer of `sentinel server`: the agent API and
// the web UI. Business rules live in package service.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/server/service"
)

type Server struct {
	Config  *config.ServerConfig
	Service *service.Service
	// Version is shown in the UI footer.
	Version string

	mux     *http.ServeMux
	limiter *loginLimiter
}

func New(cfg *config.ServerConfig, svc *service.Service) *Server {
	s := &Server{Config: cfg, Service: svc, mux: http.NewServeMux(), limiter: newLoginLimiter(5, time.Minute)}
	s.routesAgent()
	s.routesUI()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
	s.mux.ServeHTTP(w, r)
}

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
	http.Error(w, "internal error", http.StatusInternalServerError)
}
