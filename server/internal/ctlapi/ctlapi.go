// Package ctlapi is the JSON of the API of sentinelctl, shared by the server that answers it and the
// client (sentinelctl) that calls it, so the two cannot drift apart.
package ctlapi

import (
	"time"

	"sentinel/pkg/agentapi"
)

// Routes. Everything but login needs the session token in "Authorization: Bearer".
const (
	RouteLogin       = "/v1/ctl/login"
	RouteLogout      = "/v1/ctl/logout"
	RoutePasswd      = "/v1/ctl/passwd" //nolint:gosec // a route, not a credential
	RouteAgents      = "/v1/ctl/agents"
	RouteAgent       = "/v1/ctl/agents/{name}" // GET shows, DELETE revokes
	RouteAlerts      = "/v1/ctl/alerts"
	RouteAudit       = "/v1/ctl/audit"
	RouteAuditVerify = "/v1/ctl/audit/verify"
)

// ErrorBody is every error answer.
type ErrorBody struct {
	Error string `json:"error"`
}

type LoginRequest struct {
	Password string `json:"password"`
}

type LoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PasswdRequest struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// Agent is one server the control plane knows. Times that do not apply are nil.
type Agent struct {
	Name  string `json:"name"`
	State string `json:"state"` // pending, active or revoked
	// Status is what to show: PENDING, ONLINE, OFFLINE or REVOKED.
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	EnrolledAt   *time.Time `json:"enrolled_at,omitempty"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
	Version      string     `json:"version,omitempty"`
	CertNotAfter *time.Time `json:"cert_not_after,omitempty"`
}

// AgentDetail is one agent with the latest report it sent. Report is nil until it has sent one.
type AgentDetail struct {
	Agent
	Report   *agentapi.Report `json:"report,omitempty"`
	ReportAt *time.Time       `json:"report_at,omitempty"`
}

// Alert is an alert the control plane has open.
type Alert struct {
	Key     string    `json:"key"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

type AddAgentRequest struct {
	Name string `json:"name"`
}

// AddAgentResponse holds what the new agent is given to join. The token is shown once.
type AddAgentResponse struct {
	Name          string    `json:"name"`
	Token         string    `json:"token"`
	ExpiresAt     time.Time `json:"expires_at"`
	JoinURL       string    `json:"join_url"`
	CAFingerprint string    `json:"ca_fingerprint"`
}

// AuditRecord is one row of the audit log.
type AuditRecord struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Session string    `json:"session,omitempty"`
	IP      string    `json:"ip,omitempty"`
	Action  string    `json:"action"`
	Target  string    `json:"target,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// AuditVerify is the result of checking the hash chain. Error is empty when it holds.
type AuditVerify struct {
	Checked int    `json:"checked"`
	Error   string `json:"error,omitempty"`
}
