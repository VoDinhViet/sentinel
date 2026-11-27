package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/notify"
)

type Config struct {
	ServerName     string                   `yaml:"server_name"`
	StorePath      string                   `yaml:"store"`
	Notifiers      notify.Settings          `yaml:"notifiers"`
	Alert          AlertSettings            `yaml:"alert"`
	Disk           DiskSettings             `yaml:"disk"`
	Heartbeat      notify.HeartbeatSettings `yaml:"heartbeat"`
	ControlPlane   ControlPlane             `yaml:"control_plane"`
	Fleet          Fleet                    `yaml:"fleet"`
	API            API                      `yaml:"api"`
	Repositories   map[string]Repository    `yaml:"repositories" validate:"dive"`
	Jobs           []Job                    `yaml:"jobs" validate:"dive"`
	VerifySchedule string                   `yaml:"verify_schedule"`
	// PruneSchedule is when the data that forgotten snapshots held is removed from each repository.
	PruneSchedule string `yaml:"prune_schedule"`
	// Resources limits how much of the server a backup may take.
	Resources Resources `yaml:"resources"`
	// ShutdownWait is how long the daemon lets running backups finish when it is stopped.
	ShutdownWait time.Duration `yaml:"shutdown_wait" validate:"gte=0"`
	Defaults     Defaults      `yaml:"defaults"`
	UptimeChecks []UptimeCheck `yaml:"uptime" validate:"dive"`

	// EnvRefs are the variables the file refers to with ${VAR}, sorted. Set when it is parsed, not read
	// from YAML. restic and the dump tools get every value of the config explicitly, so they do not
	// inherit these (see restic.Client.DropEnv): a notifier token is not theirs to see.
	EnvRefs []string `yaml:"-"`
}

// Resources keeps backups from starving the services on the server. The defaults are the
// careful ones: backups run at low CPU and disk priority and one at a time.
type Resources struct {
	// Priority is "low" (default: nice 10 and the lowest best-effort disk priority for restic
	// and the dump tool it starts) or "normal".
	Priority string `yaml:"priority" validate:"oneof=low normal"`
	// MaxParallel is how many backups may run at once across the daemon and every
	// `sentinel backup run` on the machine; the others wait for a free slot. Default 1.
	MaxParallel int `yaml:"max_parallel" validate:"min=1"`
	// UploadLimitKiB caps the upload speed of every backup, in KiB/s (0 = no cap).
	UploadLimitKiB int `yaml:"upload_limit_kib" validate:"gte=0"`
}

// Resource priorities.
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
)

// Defaults are the values a backup job takes when it does not set its own, so a file with
// several similar jobs does not repeat them. A job's own value always wins.
//
// A job that sets none of its retention counts takes the default retention, so "keep
// everything" cannot be asked for by a job once defaults.retention is set.
type Defaults struct {
	Schedule     string    `yaml:"schedule"`
	Repositories []string  `yaml:"repositories"`
	Retention    Retention `yaml:"retention"`
}

type AlertSettings struct {
	// Cooldown is how often a still-firing alert is re-sent.
	Cooldown time.Duration `yaml:"cooldown"`
	// NotifySuccess sends a message after every successful backup.
	NotifySuccess bool `yaml:"notify_success"`
}

// ControlPlane connects this agent to a control plane (sentinel-server): the daemon reports to it every
// minute or so and renews its certificate. It is off when URL is empty, and the agent then works alone.
// The key, certificate and CA come from `sentinel agent join`; they are not in this file.
type ControlPlane struct {
	// URL is the address of the agents' port of the control plane, https://host:9320.
	URL string `yaml:"url" validate:"omitempty,https_url"`
	// Dir holds the key, certificate and CA that `sentinel agent join` wrote. Empty means the agent
	// directory next to the store.
	Dir string `yaml:"dir" validate:"omitempty,abspath"`
	// Allow is what the control plane may ask this agent to do. Everything is off until it is turned on here:
	// the control plane cannot widen it, and a change needs a restart of the daemon.
	Allow Allow `yaml:"allow"`
}

// DefaultDockerSocket is where the Docker Engine listens, when allow.docker_socket says nothing.
const DefaultDockerSocket = "/var/run/docker.sock"

// Allow is the local allowlist of the actions of agentapi. A task whose action is not allowed is refused,
// whoever asked.
type Allow struct {
	// Backup lets the control plane start and stop the backups of the jobs in this file and list their snapshots.
	Backup bool `yaml:"backup"`
	// Doctor lets it run `sentinel doctor`.
	Doctor bool `yaml:"doctor"`
	// Services are the systemd units that service.start, service.stop and service.restart may name: exactly
	// these, never a pattern.
	Services []string `yaml:"services"`
	// Containers are the Docker containers (names or ids) that container.restart may name: exactly these.
	Containers []string `yaml:"containers"`
	// DockerSocket is the unix socket of the Docker Engine; DefaultDockerSocket when empty.
	DockerSocket string `yaml:"docker_socket" validate:"omitempty,abspath"`
	// OSUpdates is off (the default), check (os.check_updates may list what could be upgraded) or apply (os.apply_updates
	// may also upgrade the packages of this machine). Only apt-based systems (Debian, Ubuntu) are supported.
	OSUpdates string `yaml:"os_updates" validate:"omitempty,oneof=off check apply"`
}

// The values of Allow.OSUpdates.
const (
	OSUpdatesOff   = "off"
	OSUpdatesCheck = "check"
	OSUpdatesApply = "apply"
)

// Permits says the action may run with these parameters. The names of a unit or a container have to be in
// the lists above.
func (a Allow) Permits(action string, params map[string]string) bool {
	switch action {
	case agentapi.ActionBackupStart, agentapi.ActionBackupStop, agentapi.ActionBackupSnapshots:
		return a.Backup
	case agentapi.ActionDoctorRun:
		return a.Doctor
	case agentapi.ActionServiceStart, agentapi.ActionServiceStop, agentapi.ActionServiceRestart:
		return slices.Contains(a.Services, params["unit"])
	case agentapi.ActionContainerRestart:
		return slices.Contains(a.Containers, params["container"])
	case agentapi.ActionOSCheckUpdates:
		return a.OSUpdates == OSUpdatesCheck || a.OSUpdates == OSUpdatesApply
	case agentapi.ActionOSApplyUpdates:
		return a.OSUpdates == OSUpdatesApply
	}
	return false
}

// Offers says the action can run for at least one name: it is what the report tells the control plane.
func (a Allow) Offers(action string) bool {
	switch action {
	case agentapi.ActionServiceStart, agentapi.ActionServiceStop, agentapi.ActionServiceRestart:
		return len(a.Services) > 0
	case agentapi.ActionContainerRestart:
		return len(a.Containers) > 0
	}
	return a.Permits(action, nil)
}

// Actions are the actions that can run, in order.
func (a Allow) Actions() []string {
	allowed := []string{}
	for _, action := range agentapi.Actions() {
		if a.Offers(action) {
			allowed = append(allowed, action)
		}
	}
	return allowed
}

// Hint says which key of the config allows the action with these parameters, for the message of a refusal.
func (Allow) Hint(action string, params map[string]string) string {
	switch action {
	case agentapi.ActionDoctorRun:
		return "control_plane.allow.doctor: true"
	case agentapi.ActionServiceStart, agentapi.ActionServiceStop, agentapi.ActionServiceRestart:
		return "control_plane.allow.services: [" + params["unit"] + "]"
	case agentapi.ActionContainerRestart:
		return "control_plane.allow.containers: [" + params["container"] + "]"
	case agentapi.ActionOSCheckUpdates:
		return "control_plane.allow.os_updates: check"
	case agentapi.ActionOSApplyUpdates:
		return "control_plane.allow.os_updates: apply"
	}
	return "control_plane.allow.backup: true"
}

// Socket is the Docker socket to use.
func (a Allow) Socket() string {
	if a.DockerSocket != "" {
		return a.DockerSocket
	}
	return DefaultDockerSocket
}

// Enabled says the daemon reports to a control plane.
func (c ControlPlane) Enabled() bool { return c.URL != "" }

// ControlPlaneDirName is the directory of the identity, inside the one the store is in. It is not under
// /etc: the unit's ProtectSystem=full makes /etc read-only, and a renewed certificate has to be written.
const ControlPlaneDirName = "agent"

// ControlPlaneDir is where the identity of this agent is kept.
func (c *Config) ControlPlaneDir() string {
	if c.ControlPlane.Dir != "" {
		return c.ControlPlane.Dir
	}
	return filepath.Join(filepath.Dir(c.StorePath), ControlPlaneDirName)
}

// Fleet is the block of the config that made the daemon watch other servers. The control plane does that
// now, so the block is still read, so that an old config keeps loading, and then ignored: Deprecated names it.
type Fleet struct {
	Config    string        `yaml:"config"`
	Every     time.Duration `yaml:"every"`
	FailAfter int           `yaml:"fail_after"`
}

// API is the block of the config that opened the daemon's HTTP API. The daemon listens on no port now,
// so the block is still read, so that an old config keeps loading, and then ignored: Deprecated names it.
type API struct {
	Listen    string `yaml:"listen"`
	Token     string `yaml:"token"`
	ReadToken string `yaml:"read_token"`
	TLSCert   string `yaml:"tls_cert"`
	TLSKey    string `yaml:"tls_key"`
}

// Deprecated names the blocks of the config that do nothing any more but are still set, for a warning.
func (c *Config) Deprecated() []string {
	var blocks []string
	if c.API != (API{}) {
		blocks = append(blocks, "api")
	}
	if c.Fleet != (Fleet{}) {
		blocks = append(blocks, "fleet")
	}
	return blocks
}

// DefaultMinFreePercent is the share of a file system that must stay free before the daemon raises
// a disk alert, when the config does not set min_free_percent.
const DefaultMinFreePercent = 10

// DiskSettings tells the daemon when a file system it writes to is too full. A backup that runs out of
// space fails half way, and so does the restic cache or the state database.
type DiskSettings struct {
	// MinFreePercent is the share of free space (0-90) below which the alert opens; 0 turns the alert
	// off. Unset means DefaultMinFreePercent, which is why it is a pointer.
	MinFreePercent *int `yaml:"min_free_percent" validate:"omitnil,gte=0,lte=90"`
	// Paths are file systems to watch besides the ones Sentinel always watches (the state database, the
	// restic cache and each local repository).
	Paths []string `yaml:"paths" validate:"dive,abspath"`
}

// MinFree is the share of free space below which the disk alert opens, 0 when it is off.
func (d DiskSettings) MinFree() int {
	if d.MinFreePercent == nil {
		return DefaultMinFreePercent
	}
	return *d.MinFreePercent
}

type Repository struct {
	URL      string            `yaml:"url" validate:"required"`
	Password string            `yaml:"password" validate:"required"`
	Env      map[string]string `yaml:"env"`
}

// Job types.
const (
	JobPostgres = "postgres"
	JobMySQL    = "mysql" // MySQL and MariaDB
	JobMongoDB  = "mongodb"
	JobFiles    = "files"
)

// Job is one configured backup: what to back up, when, and where to.
type Job struct {
	Name         string        `yaml:"name" validate:"required"`
	Type         string        `yaml:"type" validate:"oneof=postgres mysql mongodb files"`
	Schedule     string        `yaml:"schedule"`
	Timeout      time.Duration `yaml:"timeout" validate:"gte=0"`
	Postgres     *Postgres     `yaml:"postgres" validate:"required_if=Type postgres"`
	MySQL        *MySQL        `yaml:"mysql" validate:"required_if=Type mysql"`
	MongoDB      *MongoDB      `yaml:"mongodb" validate:"required_if=Type mongodb"`
	Paths        []string      `yaml:"paths"`
	Excludes     []string      `yaml:"exclude"`
	Repositories []string      `yaml:"repositories"`
	Retention    Retention     `yaml:"retention"`

	// Inherited says which values came from Defaults (or from the only repository) rather
	// than from the job's own entry; the config editor leaves those out of the file.
	Inherited Inherited `yaml:"-"`
}

// Inherited marks the values of a Job that were filled in from Defaults.
type Inherited struct {
	Schedule, Repositories, Retention bool
}

type Postgres struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database" validate:"required"`
}

// MySQL covers MySQL and MariaDB servers.
type MySQL struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database" validate:"required"`
	// TLS is "" (the client's default), "disabled" or "required". Recent
	// MariaDB clients insist on TLS and fail against a server that has none.
	TLS string `yaml:"tls" validate:"omitempty,oneof=disabled required"`
}

type MongoDB struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user" validate:"required_with=Password"`
	Password string `yaml:"password"`
	Database string `yaml:"database" validate:"required"`
	// AuthDatabase is where the user is defined (default "admin" when a user is set).
	AuthDatabase string `yaml:"auth_database"`
}

type Retention struct {
	Last    int `yaml:"last" json:"last"`
	Daily   int `yaml:"daily" json:"daily"`
	Weekly  int `yaml:"weekly" json:"weekly"`
	Monthly int `yaml:"monthly" json:"monthly"`
	Yearly  int `yaml:"yearly" json:"yearly"`
}

// negativeRule names the first rule that is below zero ("last", "daily", ...), or "" when none is.
// A negative count is not "empty", so it would reach restic as a forget with no keep rule at all.
func (r Retention) negativeRule() string {
	rules := []struct {
		name string
		keep int
	}{{"last", r.Last}, {"daily", r.Daily}, {"weekly", r.Weekly}, {"monthly", r.Monthly}, {"yearly", r.Yearly}}
	for _, rule := range rules {
		if rule.keep < 0 {
			return rule.name
		}
	}
	return ""
}

// Empty reports whether no retention rule is set (snapshots are kept forever).
func (r Retention) Empty() bool {
	return r == Retention{}
}

// String summarises the rules, e.g. "last 7 · daily 14"; "keep all" if none is set.
func (r Retention) String() string {
	var rules []string
	for _, rule := range []struct {
		name  string
		count int
	}{{"last", r.Last}, {"daily", r.Daily}, {"weekly", r.Weekly}, {"monthly", r.Monthly}, {"yearly", r.Yearly}} {
		if rule.count > 0 {
			rules = append(rules, fmt.Sprintf("%s %d", rule.name, rule.count))
		}
	}
	if len(rules) == 0 {
		return "keep all"
	}
	return strings.Join(rules, " · ")
}

// Uptime check types.
const (
	CheckHTTP = "http"
	CheckTCP  = "tcp"
	CheckPing = "ping"
)

type UptimeCheck struct {
	Name           string        `yaml:"name" validate:"required"`
	Type           string        `yaml:"type" validate:"oneof=http tcp ping"`
	URL            string        `yaml:"url" validate:"required_if=Type http"`
	Address        string        `yaml:"address" validate:"required_if=Type tcp"`
	Host           string        `yaml:"host" validate:"required_if=Type ping"`
	Interval       time.Duration `yaml:"interval"`
	Timeout        time.Duration `yaml:"timeout"`
	ExpectStatus   int           `yaml:"expect_status"`
	Keyword        string        `yaml:"keyword"`
	CertExpiryDays int           `yaml:"cert_expiry_days"`
	FailAfter      int           `yaml:"fail_after"`
}

// FindJob returns the job with the given name.
func (c *Config) FindJob(name string) (*Job, error) {
	for i := range c.Jobs {
		if c.Jobs[i].Name == name {
			return &c.Jobs[i], nil
		}
	}
	return nil, fmt.Errorf("job %q not found", name)
}

// OnlyRepo returns a copy of the job that backs up to repoName alone. The job must use it.
func (j *Job) OnlyRepo(repoName string) (*Job, error) {
	if !slices.Contains(j.Repositories, repoName) {
		return nil, fmt.Errorf("job %q does not use repository %q", j.Name, repoName)
	}
	scoped := *j
	scoped.Repositories = []string{repoName}
	return &scoped, nil
}
