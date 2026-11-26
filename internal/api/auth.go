package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authorized runs next only for a request that carries the bearer token.
func (s *Server) authorized(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// An empty token matches nothing: a server made without one refuses every request.
		if !ok || given == "" || s.token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing or wrong token")
			return
		}
		next(w, r)
	})
}
