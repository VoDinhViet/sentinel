package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"sentinel/server/internal/ctlapi"
)

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("httpapi: write response", "err", err) // the client is gone
	}
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, ctlapi.ErrorBody{Error: message})
}

// internalError answers a failure the caller cannot act on. The detail goes to the log only: it may hold
// paths or SQL.
func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error, see the server log")
}

// readJSON reads a request body of at most limit bytes into target and says whether it could. A body that
// is too large, is not JSON, has a field that is not known or has anything after the JSON is refused.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if err == nil && decoder.More() {
		err = errors.New("unexpected data after the JSON")
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "the request is too large")
		} else {
			writeError(w, http.StatusBadRequest, "the request is not the JSON this route takes")
		}
		return false
	}
	return true
}

// clientIP is the address the request came from, without the port. An IPv6 address stands for its
// whole /64, which is what one subscriber gets: otherwise a client with billions of addresses would have
// a fresh allowance of wrong passwords for each. No proxy header is believed: the server is reached
// directly, or through an SSH tunnel, where every request is from the tunnel's end.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	address = address.Unmap()
	if address.Is6() {
		prefix, _ := address.Prefix(64)
		return prefix.String()
	}
	return address.String()
}

// bearer is the token of "Authorization: Bearer <token>", or "".
func bearer(r *http.Request) string {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// logged writes one debug line per request.
func logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Debug("request", "method", r.Method, "path", r.URL.Path, "ip", clientIP(r), "took", time.Since(start))
	})
}

// recovered turns a panic in a handler into a 500 and a log line, so one bad request does not take the
// connection's goroutine down with a stack on stderr only.
func recovered(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovery := recover(); recovery != nil {
				slog.Error("handler panic", "method", r.Method, "path", r.URL.Path, "panic", recovery)
				writeError(w, http.StatusInternalServerError, "internal error, see the server log")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
