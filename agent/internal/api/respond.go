package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/backup"
)

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("api: write response", "err", err) // the client is gone
	}
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

// fail answers for an error of the service. Only the errors the caller can act on are told;
// any other reaches the log, and the caller gets no detail, as paths and repository URLs may
// be in it.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, app.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, app.ErrRunning), errors.Is(err, backup.ErrNotRunning):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("api request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error, see the daemon log")
	}
}

// logged writes one debug line per request.
func logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Debug("api request", "method", r.Method, "path", r.URL.Path, "took", time.Since(start))
	})
}
