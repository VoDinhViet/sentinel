package httpapi

import (
	"errors"
	"net/http"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/pki"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/enroll"
)

// join gives an agent a certificate for a join token. It is open: the token is the credential.
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.joinLimit.Allow(ip) {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, "too many join requests from this address")
		return
	}
	var req agentapi.JoinRequest
	if !readJSON(w, r, maxJoinBody, &req) {
		return
	}
	ctx := audit.WithActor(r.Context(), audit.Actor{Name: "unknown", IP: ip})
	joined, err := s.app.Enroll.Join(ctx, req.Token, []byte(req.CSR))
	switch {
	case errors.Is(err, enroll.ErrInvalidToken):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, enroll.ErrBadRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, agentapi.JoinResponse{
			Name: joined.Name, Cert: string(joined.CertPEM), CA: string(joined.CAPEM), NotAfter: joined.NotAfter,
		})
	}
}

// caller is an agent that showed an accepted certificate.
type caller struct {
	Name     string // from the certificate, never from the request
	NotAfter time.Time
}

// agentOnly lets a request through when it carries the verified certificate of an agent that is still
// accepted. The TLS layer has checked the chain against the CA; what only the database knows (revoked,
// agent no longer active) is checked here, on every request.
func (s *Server) agentOnly(next func(w http.ResponseWriter, r *http.Request, agent caller)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
			writeError(w, http.StatusUnauthorized, "a client certificate is required")
			return
		}
		leaf := r.TLS.VerifiedChains[0][0]
		name, err := pki.AgentName(leaf)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "the certificate is not an agent's")
			return
		}
		switch err := s.app.Enroll.Check(r.Context(), pki.SerialOf(leaf), name); {
		case errors.Is(err, enroll.ErrCertRefused):
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		case err != nil:
			internalError(w, r, err)
			return
		}
		next(w, r, caller{Name: name, NotAfter: leaf.NotAfter})
	})
}

// whoAmI says whom the control plane takes the caller for: it proves a certificate works end to end.
func (s *Server) whoAmI(w http.ResponseWriter, _ *http.Request, agent caller) {
	writeJSON(w, http.StatusOK, agentapi.WhoAmI{Name: agent.Name, NotAfter: agent.NotAfter})
}
