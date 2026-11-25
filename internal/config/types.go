package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

type Config struct {
	ServerName     string                `yaml:"server_name"`
	StorePath      string                `yaml:"store"`
	Notifiers      Notifiers             `yaml:"notifiers"`
	Alert          AlertSettings         `yaml:"alert"`
	Repositories   map[string]Repository `yaml:"repositories"`
	Jobs           []Job                 `yaml:"jobs"`
	VerifySchedule string                `yaml:"verify_schedule"`
	// Resources limits how much of the server a backup may take.
	Resources Resources `yaml:"resources"`
	// ShutdownWait is how long the daemon lets running backups finish when it is stopped.
	ShutdownWait time.Duration `yaml:"shutdown_wait"`
	Defaults     Defaults      `yaml:"defaults"`
	// Monitor is accepted for old config files and ignored: Sentinel no longer
	// monitors server resources.
	Monitor      any           `yaml:"monitor"`
	UptimeChecks []UptimeCheck `yaml:"uptime"`

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
	Priority string `yaml:"priority"`
	// MaxParallel is how many backups may run at once across the daemon and every
	// `sentinel backup run` on the machine; the others wait for a free slot. Default 1.
	MaxParallel int `yaml:"max_parallel"`
	// UploadLimitKiB caps the upload speed of every backup, in KiB/s (0 = no cap).
	UploadLimitKiB int `yaml:"upload_limit_kib"`
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

type Notifiers struct {
	Telegram *Telegram `yaml:"telegram"`
	SMTP     *SMTP     `yaml:"smtp"`
	Discord  *Webhook  `yaml:"discord"`
	Slack    *Webhook  `yaml:"slack"`
}

type Telegram struct {
	Token  string `yaml:"token"`
	ChatID string `yaml:"chat_id"`
}

type SMTP struct {
	Host string   `yaml:"host"`
	Port int      `yaml:"port"`
	User string   `yaml:"user"`
	Pass string   `yaml:"pass"`
	From string   `yaml:"from"`
	To   []string `yaml:"to"`
	// InsecurePlaintext lets a mail go out unencrypted when a server that is not this machine offers no
	// STARTTLS. Off by default: the alert text quotes file names and error output.
	InsecurePlaintext bool `yaml:"insecure_plaintext"`
}

type Webhook struct {
	URL string `yaml:"webhook"`
}

type AlertSettings struct {
	// Cooldown is how often a still-firing alert is re-sent.
	Cooldown time.Duration `yaml:"cooldown"`
	// NotifySuccess sends a message after every successful backup.
	NotifySuccess bool `yaml:"notify_success"`
}

type Repository struct {
	URL      string            `yaml:"url"`
	Password string            `yaml:"password"`
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
	Name         string        `yaml:"name"`
	Type         string        `yaml:"type"`
	Schedule     string        `yaml:"schedule"`
	Timeout      time.Duration `yaml:"timeout"`
	Postgres     *Postgres     `yaml:"postgres"`
	MySQL        *MySQL        `yaml:"mysql"`
	MongoDB      *MongoDB      `yaml:"mongodb"`
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
	Database string `yaml:"database"`
}

// MySQL covers MySQL and MariaDB servers.
type MySQL struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	// TLS is "" (the client's default), "disabled" or "required". Recent
	// MariaDB clients insist on TLS and fail against a server that has none.
	TLS string `yaml:"tls"`
}

type MongoDB struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
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
	Name           string        `yaml:"name"`
	Type           string        `yaml:"type"`
	URL            string        `yaml:"url"`
	Address        string        `yaml:"address"`
	Host           string        `yaml:"host"`
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
