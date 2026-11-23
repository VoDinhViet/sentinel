package server

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"

	"sentinel/internal/server/service"
)

type auditKey struct{}

// auditNotes collects details a handler wants in its audit entry.
type auditNotes struct {
	mu      sync.Mutex
	details map[string]any
	job     string
}

// auditNote adds a detail to the current request's audit entry. It does nothing
// outside an audited route. Never pass secrets.
func auditNote(r *http.Request, key string, value any) {
	if n, ok := r.Context().Value(auditKey{}).(*auditNotes); ok {
		n.mu.Lock()
		defer n.mu.Unlock()
		if key == "job" {
			if name, ok := value.(string); ok {
				n.job = name
				return
			}
		}
		if n.details == nil {
			n.details = map[string]any{}
		}
		n.details[key] = value
	}
}

// statusWriter remembers the response status for the audit entry.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// audited records an entry for every call of h: who, from where, which agent
// and job, and whether it worked, failed or was denied. The agent comes from
// the {id} path value.
func (s *Server) audited(action string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		notes := &auditNotes{}
		sw := &statusWriter{ResponseWriter: w}
		h(sw, r.WithContext(context.WithValue(r.Context(), auditKey{}, notes)))

		outcome := service.OutcomeOK
		switch {
		case sw.status == http.StatusForbidden:
			outcome = service.OutcomeDenied
		case sw.status >= 400:
			outcome = service.OutcomeFailed
		}
		entry := service.AuditEntry{IP: clientIP(r), Action: action, Outcome: outcome}
		notes.mu.Lock()
		entry.Details, entry.Job = notes.details, notes.job
		notes.mu.Unlock()
		if entry.Job == "" {
			entry.Job = r.PathValue("name")
		}
		// Log with a context that survives the client hanging up.
		ctx := context.WithoutCancel(r.Context())
		if id, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err == nil {
			entry.AgentID = id
			if a, err := s.Service.GetAgent(ctx, id); err == nil {
				entry.AgentName = a.Name
			}
		}
		if sw.status >= 400 {
			if entry.Details == nil {
				entry.Details = map[string]any{}
			}
			entry.Details["status"] = sw.status
		}
		if err := s.Service.Audit(service.WithActor(ctx, service.ActorFrom(r.Context())), entry); err != nil {
			slog.Error("audit", "action", action, "error", err)
		}
	}
}
