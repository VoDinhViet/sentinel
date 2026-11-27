package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authorized runs next only for a request that carries a bearer token of the server. The token
// may do anything; the read token only the read-only methods, any other gets 403. A server made
// without a token refuses everything, even a request that carries its read token.
func (s *Server) authorized(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		// Both tokens are always compared, so the time taken does not tell which one matched.
		full, read := tokenMatches(given, s.opts.Token), tokenMatches(given, s.opts.ReadToken)
		switch {
		case !ok || s.opts.Token == "" || (!full && !read):
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "missing or wrong token")
		case !full && !readOnly(r.Method):
			writeError(w, http.StatusForbidden, "this token can only read")
		default:
			next(w, r)
		}
	})
}

// tokenMatches compares in constant time. An empty token matches nothing: a server made without
// one refuses every request.
func tokenMatches(given, token string) bool {
	return given != "" && token != "" && subtle.ConstantTimeCompare([]byte(given), []byte(token)) == 1
}

// readOnly says the method changes nothing.
func readOnly(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}
