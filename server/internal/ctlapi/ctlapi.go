// Package ctlapi is the JSON of the API of sentinelctl, shared by the server that answers it and the
// client (sentinelctl) that calls it, so the two cannot drift apart.
package ctlapi

import (
	"time"

	"sentinel/pkg/agentapi"
)

// Routes. Everything but login needs the session token in "Authorization: Bearer".
const (
	RouteLogin         = "/v1/ctl/login"
	RouteLogout        = "/v1/ctl/logout"
	RoutePasswd        = "/v1/ctl/passwd" //nolint:gosec // a route, not a credential
	RouteAgents        = "/v1/ctl/agents"
	RouteAgent         = "/v1/ctl/agents/{name}" // GET shows, DELETE revokes
	RouteAlerts        = "/v1/ctl/alerts"
	RouteTasks         = "/v1/ctl/tasks"             // GET lists, POST queues one
	RouteTask          = "/v1/ctl/tasks/{id}"        // GET shows, DELETE takes back one that is waiting
	RouteTaskEvents    = "/v1/ctl/tasks/{id}/events" // text/event-stream: state, log and done events
	RouteSchedules     = "/v1/ctl/schedules"         // GET lists, POST makes one
	RouteSchedule      = "/v1/ctl/schedules/{id}"    // DELETE removes
	RouteMaintenance   = "/v1/ctl/maintenance"       // GET lists the windows that are open or to come, POST opens one
	RouteWindow        = "/v1/ctl/maintenance/{id}"  // DELETE removes (ends) one
	RouteReportHost    = "/v1/ctl/reports/host"      // ?agent=&hours=
	RouteReportBackups = "/v1/ctl/reports/backups"   // ?agent=&hours=
	RouteAudit         = "/v1/ctl/audit"
	RouteAuditVerify   = "/v1/ctl/audit/verify"
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
	// Status is what to show: PENDING, ONLINE, OFFLINE, REVOKED, or MAINTENANCE while a maintenance window is open.
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

// Task is one thing an agent was asked to do.
type Task struct {
	ID         int64             `json:"id"`
	Agent      string            `json:"agent"`
	Action     string            `json:"action"`
	Params     map[string]string `json:"params"`
	State      string            `json:"state"` // queued, sent, succeeded, failed, refused, canceled or expired
	CreatedAt  time.Time         `json:"created_at"`
	CreatedBy  string            `json:"created_by"`
	SentAt     *time.Time        `json:"sent_at,omitempty"`
	StartedAt  *time.Time        `json:"started_at,omitempty"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	Output     string            `json:"output,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// TaskLine is a line of the log of a task.
type TaskLine struct {
	Seq  int64     `json:"seq"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// TaskDetail is a task with the log its agent wrote while it ran.
type TaskDetail struct {
	Task
	Log []TaskLine `json:"log"`
}

// CreateTaskRequest queues an action for one server.
type CreateTaskRequest struct {
	Agent  string            `json:"agent"`
	Action string            `json:"action"`
	Params map[string]string `json:"params"`
}

// The events of RouteTaskEvents. A "log" event has the sequence number of the line as its id, and its
// data is a TaskLine, so a client that was cut off resumes with Last-Event-ID. "state" has {"state": …}
// and "done" is the Task as it ended, after which the stream closes.
const (
	EventState = "state"
	EventLog   = "log"
	EventDone  = "done"
)

// Schedule is a task the control plane queues by timetable. Next is nil when the timetable has no next run.
type Schedule struct {
	ID        int64             `json:"id"`
	Agent     string            `json:"agent"`
	Action    string            `json:"action"`
	Params    map[string]string `json:"params"`
	Spec      string            `json:"spec"`
	Next      *time.Time        `json:"next,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	CreatedBy string            `json:"created_by"`
	LastRunAt *time.Time        `json:"last_run_at,omitempty"`
	LastTask  *int64            `json:"last_task,omitempty"`
	LastError string            `json:"last_error,omitempty"`
}

// CreateScheduleRequest makes a schedule: Spec is a cron timetable.
type CreateScheduleRequest struct {
	Agent  string            `json:"agent"`
	Spec   string            `json:"spec"`
	Action string            `json:"action"`
	Params map[string]string `json:"params"`
}

// Window is a maintenance window. Agent is empty when it is for every server; Open says it is open now.
type Window struct {
	ID        int64     `json:"id"`
	Agent     string    `json:"agent,omitempty"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Reason    string    `json:"reason,omitempty"`
	Open      bool      `json:"open"`
	CreatedBy string    `json:"created_by"`
}

// CreateWindowRequest opens a window: StartsAt nil means now.
type CreateWindowRequest struct {
	// Agent names the server; All says every server instead. One of the two is needed, never both: an empty
	// name must not turn into a window for everything.
	Agent    string     `json:"agent"`
	All      bool       `json:"all"`
	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   time.Time  `json:"ends_at"`
	Reason   string     `json:"reason"`
}

// HostPoint is the host at one moment, or the average of a step of the history.
type HostPoint struct {
	At          time.Time `json:"at"`
	CPUPercent  float64   `json:"cpu_percent"`
	Load1       float64   `json:"load1"`
	MemPercent  float64   `json:"mem_percent"`
	DiskPercent float64   `json:"disk_percent"` // of the fullest disk
}

// HostSummary is a span of the history in a few numbers.
type HostSummary struct {
	Samples int     `json:"samples"`
	CPUAvg  float64 `json:"cpu_avg"`
	CPUMax  float64 `json:"cpu_max"`
	MemAvg  float64 `json:"mem_avg"`
	MemMax  float64 `json:"mem_max"`
	LoadAvg float64 `json:"load_avg"`
	DiskMax float64 `json:"disk_max"`
}

// HostReport is the history of the host of one server over the last Hours hours. A point is the average of
// StepSeconds seconds (the disk is the highest in the step).
type HostReport struct {
	Agent       string      `json:"agent"`
	Hours       int         `json:"hours"`
	StepSeconds int         `json:"step_s"`
	Summary     HostSummary `json:"summary"`
	Points      []HostPoint `json:"points"`
}

// BackupJob is how one job of one server to one repository went over the span of a report.
type BackupJob struct {
	Agent          string     `json:"agent"`
	Job            string     `json:"job"`
	Repository     string     `json:"repository"`
	Runs           int        `json:"runs"`
	Failures       int        `json:"failures"`
	LastFinishedAt time.Time  `json:"last_finished_at"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastResult     string     `json:"last_result"`
	BytesAdded     int64      `json:"bytes_added"`
}

// BackupRun is one backup that failed.
type BackupRun struct {
	Agent      string    `json:"agent"`
	Job        string    `json:"job"`
	Repository string    `json:"repository"`
	FinishedAt time.Time `json:"finished_at"`
	Result     string    `json:"result"`
}

// BackupReport is the backups of one server, or of all, over the last Hours hours, with the failed runs
// newest first.
type BackupReport struct {
	Agent  string      `json:"agent,omitempty"`
	Hours  int         `json:"hours"`
	Jobs   []BackupJob `json:"jobs"`
	Failed []BackupRun `json:"failed"`
}
