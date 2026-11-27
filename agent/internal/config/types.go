package config

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"sentinel/pkg/notify"
)

type Config struct {
	ServerName     string                   `yaml:"server_name"`
	StorePath      string                   `yaml:"store"`
	Notifiers      notify.Settings          `yaml:"notifiers"`
	Alert          AlertSettings            `yaml:"alert"`
	Disk           DiskSettings             `yaml:"disk"`
	Heartbeat      notify.HeartbeatSettings `yaml:"heartbeat"`
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

// DefaultFleetEvery is how often the servers of the fleet are asked when the config sets no interval.
const DefaultFleetEvery = time.Minute

// DefaultFleetFailAfter is how many rounds in a row a server must fail before the daemon alerts.
const DefaultFleetFailAfter = 3

// Fleet makes the daemon watch other servers: every Every it asks the API of each server in the
// fleet file at Config, and alerts when one cannot be reached FailAfter times in a row. It is off
// when Config is empty. The servers are the ones `sentinel fleet` shows.
type Fleet struct {
	// Config is the fleet file (see package fleet); an absolute path.
	Config    string        `yaml:"config" validate:"omitempty,abspath"`
	Every     time.Duration `yaml:"every" validate:"omitempty,gte=30s"`
	FailAfter int           `yaml:"fail_after" validate:"omitempty,gte=1"`
}

// API is the HTTP API of the daemon (package api). It is off when Listen is empty.
type API struct {
	// Listen is the host:port to listen on, e.g. 127.0.0.1:9311.
	Listen string `yaml:"listen" validate:"omitempty,listen_addr"`
	// Token is the bearer token every /api/ request must carry; write it as ${SENTINEL_API_TOKEN} and
	// keep the value in the env file.
	Token string `yaml:"token" validate:"required_with=Listen,omitempty,api_token"`
	// ReadToken is a second bearer token that may only read (GET): a machine that watches several
	// servers needs no power to start or stop a backup. Write it as ${SENTINEL_API_READ_TOKEN}. Empty, the
	// API has only Token.
	ReadToken string `yaml:"read_token" validate:"omitempty,api_token,nefield=Token"`
	// TLSCert and TLSKey are the PEM files of a certificate and its private key: with both set the API
	// speaks HTTPS (TLS 1.2 or later) instead of plain HTTP. They are read when the daemon starts, so a
	// renewed certificate needs a restart.
	TLSCert string `yaml:"tls_cert" validate:"required_with=TLSKey,omitempty,abspath"`
	TLSKey  string `yaml:"tls_key" validate:"required_with=TLSCert,omitempty,abspath"`
}

// TLS says the API is served over HTTPS.
func (a API) TLS() bool { return a.TLSCert != "" && a.TLSKey != "" }

// MinTokenLength is the shortest API token accepted.
const MinTokenLength = 16

// Loopback says the API listens on this machine only: 127.0.0.0/8, ::1 or localhost.
func (a API) Loopback() bool {
	host, _, err := net.SplitHostPort(a.Listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
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
