package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"sentinel/pkg/pki"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/auth"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/enroll"
)

// Audit pages.
const (
	defaultAuditLimit = 50
	maxAuditLimit     = 1000
)

// limited refuses an address that calls too often.
func (s *Server) limited(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.ctlLimit.Allow(clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		next(w, r)
	})
}

// session is the signed-in session of a request.
type session struct {
	Token string
	ID    string
}

// signedIn lets a request through when it carries the token of a session that has not ended, and
// records the session and the address as the actor of what the handler does. A
// token that is no session is answered 401 without more: those requests are not audited, because
// anybody who can reach the port can send them and the log never shrinks.
func (s *Server) signedIn(next func(w http.ResponseWriter, r *http.Request, who session)) http.Handler {
	return s.limited(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "sign in first: sentinelctl login")
			return
		}
		current, err := s.app.Auth.Authenticate(r.Context(), token)
		switch {
		case errors.Is(err, auth.ErrUnauthorized):
			writeError(w, http.StatusUnauthorized, "the session has ended: sentinelctl login")
			return
		case err != nil:
			internalError(w, r, err)
			return
		}
		actor := audit.Actor{Name: "ctl", Session: current.ID, IP: clientIP(r)}
		next(w, r.WithContext(audit.WithActor(r.Context(), actor)), session{Token: token, ID: current.ID})
	})
}

// authError answers an error of a sign-in or of a change of password.
func authError(w http.ResponseWriter, r *http.Request, err error) {
	var blocked *auth.BlockedError
	switch {
	case errors.As(err, &blocked):
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(time.Until(blocked.Until).Seconds()))))
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, auth.ErrBadPassword):
		writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, auth.ErrPasswordRefused):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrNotInitialized):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		internalError(w, r, err)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req ctlapi.LoginRequest
	if !readJSON(w, r, maxCtlBody, &req) {
		return
	}
	token, current, err := s.app.Auth.Login(r.Context(), clientIP(r), req.Password)
	if err != nil {
		authError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ctlapi.LoginResponse{Token: token, ExpiresAt: current.ExpiresAt})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, who session) {
	if err := s.app.Auth.Logout(r.Context(), who.Token); err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) passwd(w http.ResponseWriter, r *http.Request, _ session) {
	var req ctlapi.PasswdRequest
	if !readJSON(w, r, maxCtlBody, &req) {
		return
	}
	if err := s.app.Auth.ChangePassword(r.Context(), clientIP(r), req.Old, req.New); err != nil {
		if errors.Is(err, auth.ErrBadPassword) {
			// Not 401: the session is good, and 401 tells a client the session has ended.
			writeError(w, http.StatusForbidden, "the old password is wrong")
			return
		}
		authError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent) // every session has ended, this one too
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request, _ session) {
	agents, err := s.app.Enroll.List(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	listed := make([]ctlapi.Agent, 0, len(agents))
	for _, agent := range agents {
		listed = append(listed, ctlapi.Agent{
			Name: agent.Name, State: agent.State, CreatedAt: agent.CreatedAt,
			EnrolledAt: timePointer(agent.EnrolledAt), CertNotAfter: timePointer(agent.CertNotAfter),
		})
	}
	writeJSON(w, http.StatusOK, listed)
}

func timePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (s *Server) addAgent(w http.ResponseWriter, r *http.Request, _ session) {
	var req ctlapi.AddAgentRequest
	if !readJSON(w, r, maxCtlBody, &req) {
		return
	}
	if !pki.ValidName(req.Name) {
		writeError(w, http.StatusBadRequest, "the name may have letters, digits, '.', '_' and '-', at most 63, and starts with a letter or digit")
		return
	}
	added, err := s.app.AddAgent(r.Context(), req.Name)
	switch {
	case errors.Is(err, enroll.ErrExists):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, ctlapi.AddAgentResponse{
			Name: added.Name, Token: added.Token, ExpiresAt: added.Expires, JoinURL: added.JoinURL, CAFingerprint: added.Fingerprint,
		})
	}
}

func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request, _ session) {
	switch err := s.app.Enroll.Revoke(r.Context(), r.PathValue("name")); {
	case errors.Is(err, enroll.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case err != nil:
		internalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, _ session) {
	limit := defaultAuditLimit
	if text := r.URL.Query().Get("limit"); text != "" {
		number, err := strconv.Atoi(text)
		if err != nil || number < 1 || number > maxAuditLimit {
			writeError(w, http.StatusBadRequest, "limit must be a number from 1 to "+strconv.Itoa(maxAuditLimit))
			return
		}
		limit = number
	}
	records, err := s.app.Audit(r.Context(), limit)
	if err != nil {
		internalError(w, r, err)
		return
	}
	listed := make([]ctlapi.AuditRecord, 0, len(records))
	for _, record := range records {
		listed = append(listed, ctlapi.AuditRecord{
			ID: record.ID, At: record.At, Actor: record.Actor.Name, Session: record.Actor.Session, IP: record.Actor.IP,
			Action: record.Entry.Action, Target: record.Entry.Target, Detail: record.Entry.Detail,
		})
	}
	writeJSON(w, http.StatusOK, listed)
}

func (s *Server) verifyAudit(w http.ResponseWriter, r *http.Request, _ session) {
	checked, err := s.app.VerifyAudit(r.Context())
	result := ctlapi.AuditVerify{Checked: checked}
	if err != nil {
		result.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, result)
}
