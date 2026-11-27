package agentapi

import (
	"time"

	"sentinel/pkg/termtext"
)

// Routes of an agent that has a certificate.
const (
	RouteReport = "/v1/agent/report"
	RouteRenew  = "/v1/agent/renew"
)

// The most of each list a report may carry, and the longest text in it. The control plane takes a
// report from a machine it does not own, so it keeps to these however large the body was.
const (
	MaxReportItems   = 200
	maxReportText    = 300
	DefaultReportGap = 60 // seconds between two reports, when the control plane does not say
)

// Report is what an agent tells the control plane about itself every minute or so: how the machine is,
// how its backups and checks stand, and which alerts are open on it.
type Report struct {
	Version string     `json:"version"`
	SentAt  time.Time  `json:"sent_at"`
	Host    HostStatus `json:"host"`
	// Backups holds the latest run of every job into every repository.
	Backups []BackupStatus `json:"backups"`
	Checks  []CheckStatus  `json:"checks"`
	Alerts  []AlertStatus  `json:"alerts"`
}

// BackupStatus is the latest run of one job into one repository.
type BackupStatus struct {
	Job        string    `json:"job"`
	Repository string    `json:"repository"`
	FinishedAt time.Time `json:"finished_at"`
	Success    bool      `json:"success"`
	Result     string    `json:"result"` // "ok", "ok (warning: …)" or "FAILED: …"
	BytesAdded int64     `json:"bytes_added"`
	TotalBytes int64     `json:"total_bytes"`
}

// CheckStatus is the latest result of one uptime check.
type CheckStatus struct {
	Name      string    `json:"name"`
	OK        bool      `json:"ok"`
	LatencyMs int64     `json:"latency_ms"`
	At        time.Time `json:"at"`
	Error     string    `json:"error,omitempty"`
}

// AlertStatus is an alert that is open on the agent.
type AlertStatus struct {
	Key     string    `json:"key"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"`
}

// Clean returns the report with every text stripped of escape sequences and control characters and cut
// short, every list a list (never null) and no longer than MaxReportItems. Whoever receives a report
// calls it before storing or showing it. The report it was called on is not changed.
func (r Report) Clean() Report {
	r.Version = short(r.Version)
	r.Host = r.Host.Clean()
	r.Backups = cleanEach(first(r.Backups), func(backup BackupStatus) BackupStatus {
		backup.Job, backup.Repository, backup.Result = short(backup.Job), short(backup.Repository), short(backup.Result)
		return backup
	})
	r.Checks = cleanEach(first(r.Checks), func(check CheckStatus) CheckStatus {
		check.Name, check.Error = short(check.Name), short(check.Error)
		return check
	})
	r.Alerts = cleanEach(first(r.Alerts), func(alert AlertStatus) AlertStatus {
		alert.Key, alert.Message = short(alert.Key), short(alert.Message)
		return alert
	})
	return r
}

func short(text string) string { return termtext.Shorten(termtext.Line(text), maxReportText) }

// first is the start of a list that is at most MaxReportItems long.
func first[T any](items []T) []T {
	if len(items) > MaxReportItems {
		return items[:MaxReportItems]
	}
	return items
}

// ReportResponse is the answer to a report. ServerTime lets an agent notice that its clock is far off,
// which would make its certificate look expired or not yet valid.
type ReportResponse struct {
	ServerTime         time.Time `json:"server_time"`
	ReportEverySeconds int       `json:"report_every_s"`
}

// RenewRequest asks for a new certificate: a request (PEM) that holds nothing but a new public key.
type RenewRequest struct {
	CSR string `json:"csr"`
}

// RenewResponse is the new certificate (PEM) for the agent's name.
type RenewResponse struct {
	Cert     string    `json:"cert"`
	NotAfter time.Time `json:"not_after"`
}
