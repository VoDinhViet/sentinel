package agentapi

import "time"

// Routes between an agent and the control plane. Joining is the one route that needs no certificate.
const (
	RouteJoin   = "/v1/enroll/join"
	RouteWhoAmI = "/v1/agent/whoami"
)

// JoinRequest is what an agent sends to become an agent: the join token it was given and a
// request for a certificate (PEM) that holds nothing but its public key.
type JoinRequest struct {
	Token string `json:"token"`
	CSR   string `json:"csr"`
}

// JoinResponse is what the control plane answers: the certificate it signed (PEM), its own CA (PEM) and
// the name it gave the agent, which is the one in the certificate.
type JoinResponse struct {
	Name     string    `json:"name"`
	Cert     string    `json:"cert"`
	CA       string    `json:"ca"`
	NotAfter time.Time `json:"not_after"`
}

// WhoAmI is the answer to GET RouteWhoAmI: the name the control plane read from the agent's certificate.
type WhoAmI struct {
	Name     string    `json:"name"`
	NotAfter time.Time `json:"cert_not_after"`
}
