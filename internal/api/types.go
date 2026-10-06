// Package api defines the JSON messages exchanged between agents and the
// sentinel server. Agents always initiate: they push reports, long-poll for
// commands and poll for configuration.
package api

import (
	"encoding/json"
	"time"

	"sentinel/internal/store"
)

// MaxBatch caps how many rows of each kind one report carries.
const MaxBatch = 500

// Report is sent by an agent every few seconds. Rows are incremental: each
// slice holds only rows newer than the cursor the server last acknowledged.
type Report struct {
	Hostname   string              `json:"hostname"`
	Version    string              `json:"version"`
	Jobs       []Job               `json:"jobs"`
	Checks     []string            `json:"checks"` // configured uptime check names
	BackupRuns []store.BackupRun   `json:"backup_runs"`
	Results    []store.CheckResult `json:"check_results"`
	Metrics    []store.Metrics     `json:"metrics"`
	// OpenAlerts is the full set of currently firing alerts (not incremental).
	OpenAlerts []store.Alert `json:"open_alerts"`
	// ConfigVersion is the managed config version the agent is running (0 = local file).
	ConfigVersion int64 `json:"config_version"`
}

type Job struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Schedule     string   `json:"schedule"`
	Repositories []string `json:"repositories"`
}

// Cursor is the highest row id the server has stored, per kind.
type Cursor struct {
	BackupRunID int64 `json:"backup_run_id"`
	CheckID     int64 `json:"check_id"`
	MetricID    int64 `json:"metric_id"`
}

// ReportResponse acknowledges a report.
type ReportResponse struct {
	Cursor Cursor `json:"cursor"`
}

const (
	CmdBackupRun = "backup_run"
	CmdSnapshots = "snapshots"
	CmdRestore   = "restore"
)

const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
	StatusTimeout = "timeout"
)

type Command struct {
	ID   int64           `json:"id"`
	Type string          `json:"type"`
	Args json.RawMessage `json:"args"`
}

type BackupRunArgs struct {
	Job string `json:"job"`
}

type SnapshotsArgs struct {
	Job        string `json:"job"`
	Repository string `json:"repository"`
}

type RestoreArgs struct {
	Job        string `json:"job"`
	Repository string `json:"repository"`
	Snapshot   string `json:"snapshot"`
}

// CommandResult is posted by the agent when a command finishes. Output is
// command-specific JSON (e.g. []Snapshot for snapshots, the restore path).
type CommandResult struct {
	OK     bool            `json:"ok"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Size  int64     `json:"size"`
	Paths []string  `json:"paths"`
}

type RestoreOutput struct {
	Path string `json:"path"`
}

// ConfigDoc is a managed configuration version.
type ConfigDoc struct {
	Version int64  `json:"version"`
	YAML    string `json:"yaml"`
}

// ConfigStatus reports whether the agent could apply a config version.
type ConfigStatus struct {
	Version int64  `json:"version"`
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}
