package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"sentinel/internal/server/service"
)

const (
	sessionCookie = "sentinel_session"
	sessionTTL    = 12 * time.Hour
)

const csrfKey ctxKey = iota + 100

// requireLogin answers anonymous requests with 401 and enforces the CSRF
// header on every state-changing request. Responses are never cached.
func (s *Server) requireLogin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		c, err := r.Cookie(sessionCookie)
		var csrf string
		var actor service.Actor
		if err == nil {
			csrf, actor, err = s.Service.Session(r.Context(), c.Value)
		}
		if errors.Is(err, service.ErrUnauthorized) || errors.Is(err, http.ErrNoCookie) {
			jsonError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(csrf)) != 1 {
				jsonError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
		}
		if actor.UserID == 0 && actor.Username == "" {
			actor.Username = s.Config.Admin.Username // session from before users existed
		}
		ctx := context.WithValue(r.Context(), csrfKey, csrf)
		h(w, r.WithContext(service.WithActor(ctx, actor)))
	}
}

// requireRole answers 403 unless the logged-in user has at least role.
// It must run inside requireLogin.
func (s *Server) requireRole(role string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !service.RoleAtLeast(service.ActorFrom(r.Context()).Role, role) {
			jsonError(w, http.StatusForbidden, "Your role does not allow this.")
			return
		}
		h(w, r)
	}
}

// ui registers a logged-in endpoint that needs at least role.
func (s *Server) ui(role string, h http.HandlerFunc) http.HandlerFunc {
	return s.requireLogin(s.requireRole(role, h))
}

// uiAudited is ui plus an audit log entry for every call, including denied
// and failed ones.
func (s *Server) uiAudited(role, action string, h http.HandlerFunc) http.HandlerFunc {
	return s.requireLogin(s.audited(action, s.requireRole(role, h)))
}

func csrfFrom(r *http.Request) string {
	v, _ := r.Context().Value(csrfKey).(string)
	return v
}

type sessionInfo struct {
	CSRF     string `json:"csrf"`
	Version  string `json:"version"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	actor := service.ActorFrom(r.Context())
	writeJSON(w, http.StatusOK, sessionInfo{CSRF: csrfFrom(r), Version: s.Version, Username: actor.Username, Role: actor.Role})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ip := clientIP(r)
	if allowed, firstRefusal := s.limiter.allow(ip); !allowed {
		if firstRefusal {
			s.auditLogin(r, service.Actor{Username: "(rate limited)"}, service.OutcomeDenied)
		}
		jsonError(w, http.StatusTooManyRequests, "Too many attempts. Wait a minute and try again.")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, 4<<10, &in) {
		return
	}
	// The admin from server.yaml is the bootstrap account; everyone else is in
	// the users table. Always run bcrypt so timing does not reveal whether the
	// username exists.
	userOK := subtle.ConstantTimeCompare([]byte(in.Username), []byte(s.Config.Admin.Username)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(s.Config.Admin.PasswordHash), []byte(in.Password)) == nil
	actor := service.Actor{Username: s.Config.Admin.Username, Role: service.RoleAdmin}
	if !userOK || !passOK {
		var err error
		if actor, err = s.Service.VerifyUser(r.Context(), in.Username, in.Password); err != nil {
			s.auditLogin(r, service.Actor{Username: truncateRunes(in.Username, maxLoggedUsername)}, service.OutcomeFailed)
			jsonError(w, http.StatusUnauthorized, "Wrong username or password.")
			return
		}
	}
	s.limiter.reset(ip)
	token, csrf, err := s.Service.CreateSession(r.Context(), actor, sessionTTL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.auditLogin(r, actor, service.OutcomeOK)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: *s.Config.SecureCookies, SameSite: http.SameSiteStrictMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, sessionInfo{CSRF: csrf, Version: s.Version, Username: actor.Username, Role: actor.Role})
}

// maxLoggedUsername bounds what an unauthenticated caller can write into the
// append-only audit log through a failed login.
const maxLoggedUsername = 64

func truncateRunes(value string, limit int) string {
	if runes := []rune(value); len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func (s *Server) auditLogin(r *http.Request, actor service.Actor, outcome string) {
	err := s.Service.Audit(r.Context(), service.AuditEntry{
		UserID: actor.UserID, Username: actor.Username, IP: clientIP(r), Action: "auth.login", Outcome: outcome,
	})
	if err != nil {
		slog.Error("audit login", "error", err)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Service.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: *s.Config.SecureCookies, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// clientIP uses the TCP peer address. X-Forwarded-For is ignored because it
// can be forged; behind a reverse proxy all users share the proxy's limit.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginLimiter allows max attempts per key within window.
type loginLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu       sync.Mutex
	attempts map[string][]time.Time
	blocked  map[string]bool // keys already reported as blocked in this burst
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, now: time.Now, attempts: map[string][]time.Time{}, blocked: map[string]bool{}}
}

// allow records an attempt. When it is refused, firstRefusal is true only for
// the first refusal of a burst, so the caller can audit it once instead of once
// per request.
func (l *loginLimiter) allow(key string) (allowed, firstRefusal bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	recent := l.attempts[key][:0]
	for _, t := range l.attempts[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.max {
		l.attempts[key] = recent
		firstRefusal = !l.blocked[key]
		l.blocked[key] = true
		return false, firstRefusal
	}
	delete(l.blocked, key)
	l.attempts[key] = append(recent, now)
	return true, false
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
	delete(l.blocked, key)
}
