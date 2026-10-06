package server

import (
	"context"
	"crypto/subtle"
	"errors"
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

// requireLogin redirects anonymous users to /login and enforces CSRF tokens
// on every state-changing request.
func (s *Server) requireLogin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		var csrf string
		if err == nil {
			csrf, err = s.Service.Session(r.Context(), c.Value)
		}
		if errors.Is(err, service.ErrUnauthorized) || errors.Is(err, http.ErrNoCookie) {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			sent := r.Header.Get("X-CSRF-Token")
			if sent == "" {
				sent = r.PostFormValue("csrf_token")
			}
			if subtle.ConstantTimeCompare([]byte(sent), []byte(csrf)) != 1 {
				http.Error(w, "invalid CSRF token", http.StatusForbidden)
				return
			}
		}
		h(w, r.WithContext(context.WithValue(r.Context(), csrfKey, csrf)))
	}
}

func csrfFrom(r *http.Request) string {
	v, _ := r.Context().Value(csrfKey).(string)
	return v
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "login.html", map[string]any{"Error": ""})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.render(w, r, "login.html", map[string]any{"Error": "Too many attempts. Wait a minute and try again."})
		return
	}
	user, pass := r.PostFormValue("username"), r.PostFormValue("password")
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.Config.Admin.Username)) == 1
	// Always run bcrypt so timing does not reveal whether the username exists.
	passOK := bcrypt.CompareHashAndPassword([]byte(s.Config.Admin.PasswordHash), []byte(pass)) == nil
	if !userOK || !passOK {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, r, "login.html", map[string]any{"Error": "Wrong username or password."})
		return
	}
	s.limiter.reset(ip)
	token, _, err := s.Service.CreateSession(r.Context(), sessionTTL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: *s.Config.SecureCookies, SameSite: http.SameSiteStrictMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/manage", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Service.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: *s.Config.SecureCookies, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
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

// loginLimiter allows max failed-or-not attempts per key within window.
type loginLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, now: time.Now, attempts: map[string][]time.Time{}}
}

func (l *loginLimiter) allow(key string) bool {
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
		return false
	}
	l.attempts[key] = append(recent, now)
	return true
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
