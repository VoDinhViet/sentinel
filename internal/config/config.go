// Package config loads and validates the sentinel YAML configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

type Config struct {
	ServerName        string                `yaml:"server_name"`
	Store             string                `yaml:"store"`
	Notifiers         Notifiers             `yaml:"notifiers"`
	Alert             AlertSettings         `yaml:"alert"`
	Repositories      map[string]Repository `yaml:"repositories"`
	Backups           []Backup              `yaml:"backups"`
	RepoCheckSchedule string                `yaml:"repo_check_schedule"`
	Monitor           *Monitor              `yaml:"monitor"`
	Uptime            []UptimeCheck         `yaml:"uptime"`

	// Central connects this agent to a sentinel server.
	Central *Central `yaml:"central"`
	// RestoreDir is the only place restores requested from the server may write.
	RestoreDir string `yaml:"restore_dir"`
}

type Central struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
	// Managed means backups, monitor, uptime and alert settings come from the
	// server; this file then only needs server_name, store and central.
	Managed bool `yaml:"managed"`
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
}

type Webhook struct {
	Webhook string `yaml:"webhook"`
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

const (
	BackupPostgres = "postgres"
	BackupFiles    = "files"
)

type Backup struct {
	Name         string        `yaml:"name"`
	Type         string        `yaml:"type"`
	Schedule     string        `yaml:"schedule"`
	Timeout      time.Duration `yaml:"timeout"`
	Postgres     *Postgres     `yaml:"postgres"`
	Paths        []string      `yaml:"paths"`
	Exclude      []string      `yaml:"exclude"`
	Repositories []string      `yaml:"repositories"`
	Retention    Retention     `yaml:"retention"`
}

type Postgres struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
}

type Retention struct {
	Last    int `yaml:"last"`
	Daily   int `yaml:"daily"`
	Weekly  int `yaml:"weekly"`
	Monthly int `yaml:"monthly"`
	Yearly  int `yaml:"yearly"`
}

// Empty reports whether no retention rule is set (snapshots are kept forever).
func (r Retention) Empty() bool {
	return r == Retention{}
}

type Monitor struct {
	Interval   time.Duration `yaml:"interval"`
	Thresholds Thresholds    `yaml:"thresholds"`
}

type Thresholds struct {
	CPU         *Threshold `yaml:"cpu"`
	Memory      *Threshold `yaml:"memory"`
	Disk        *Threshold `yaml:"disk"`
	LoadPerCore *Threshold `yaml:"load_per_core"`
}

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

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv replaces ${VAR} references in every scalar value of the document
// (not in comments or keys). Unset variables are an error so a missing secret
// is caught at startup instead of producing an empty password. Expanding after
// parsing means a secret containing YAML syntax cannot change the structure.
func expandEnv(n *yaml.Node, missing map[string]bool) {
	if n.Kind == yaml.ScalarNode {
		n.Value = envRef.ReplaceAllStringFunc(n.Value, func(m string) string {
			name := envRef.FindStringSubmatch(m)[1]
			v, ok := os.LookupEnv(name)
			if !ok {
				missing[name] = true
			}
			return v
		})
		return
	}
	for i, c := range n.Content {
		if n.Kind == yaml.MappingNode && i%2 == 0 {
			continue // mapping key
		}
		expandEnv(c, missing)
	}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	return ParseWith(raw, ParseOptions{ExpandEnv: true})
}

type ParseOptions struct {
	// ExpandEnv replaces ${VAR} references. The server validates agent configs
	// with it off, since secrets only exist in each agent's environment.
	ExpandEnv bool
}

func ParseWith(raw []byte, opts ParseOptions) (*Config, error) {
	var c Config
	if err := decodeStrict(raw, &c, opts.ExpandEnv); err != nil {
		return nil, err
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// decodeStrict decodes YAML into out, optionally expanding ${VAR} in values,
// and rejects unknown fields.
func decodeStrict(raw []byte, out any, expand bool) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil
	}
	if expand {
		missing := map[string]bool{}
		expandEnv(&doc, missing)
		if len(missing) > 0 {
			return fmt.Errorf("unset environment variables: %v", slices.Sorted(maps.Keys(missing)))
		}
	}
	// Re-encode so the strict decoder can reject unknown fields.
	expanded, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.ServerName == "" {
		c.ServerName, _ = os.Hostname()
	}
	if c.Store == "" {
		c.Store = "/var/lib/sentinel/state.db"
	}
	if c.RestoreDir == "" {
		c.RestoreDir = "/var/lib/sentinel/restore"
	}
	if c.Alert.Cooldown == 0 {
		c.Alert.Cooldown = time.Hour
	}
	for i := range c.Backups {
		b := &c.Backups[i]
		if b.Timeout == 0 {
			b.Timeout = 6 * time.Hour
		}
		if b.Postgres != nil {
			if b.Postgres.Host == "" {
				b.Postgres.Host = "localhost"
			}
			if b.Postgres.Port == 0 {
				b.Postgres.Port = 5432
			}
			if b.Postgres.User == "" {
				b.Postgres.User = "postgres"
			}
		}
	}
	if c.Monitor != nil && c.Monitor.Interval == 0 {
		c.Monitor.Interval = time.Minute
	}
	for i := range c.Uptime {
		u := &c.Uptime[i]
		if u.Interval == 0 {
			u.Interval = time.Minute
		}
		if u.Timeout == 0 {
			u.Timeout = 10 * time.Second
		}
		if u.FailAfter == 0 {
			u.FailAfter = 3
		}
		if u.Type == CheckHTTP && u.ExpectStatus == 0 {
			u.ExpectStatus = 200
		}
	}
	if c.Notifiers.SMTP != nil {
		if c.Notifiers.SMTP.Port == 0 {
			c.Notifiers.SMTP.Port = 587
		}
		if c.Notifiers.SMTP.From == "" {
			c.Notifiers.SMTP.From = c.Notifiers.SMTP.User
		}
	}
}

func (c *Config) validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	for name, r := range c.Repositories {
		if r.URL == "" {
			add("repository %q: url is required", name)
		}
		if r.Password == "" {
			add("repository %q: password is required", name)
		}
	}

	seen := map[string]bool{}
	for i, b := range c.Backups {
		where := fmt.Sprintf("backups[%d] %q", i, b.Name)
		if b.Name == "" {
			add("backups[%d]: name is required", i)
		} else if seen[b.Name] {
			add("%s: duplicate name", where)
		}
		seen[b.Name] = true
		if b.Schedule != "" {
			if _, err := cron.ParseStandard(b.Schedule); err != nil {
				add("%s: invalid schedule: %v", where, err)
			}
		}
		switch b.Type {
		case BackupPostgres:
			if b.Postgres == nil || b.Postgres.Database == "" {
				add("%s: postgres.database is required", where)
			}
		case BackupFiles:
			if len(b.Paths) == 0 {
				add("%s: paths is required", where)
			}
		default:
			add("%s: type must be %q or %q", where, BackupPostgres, BackupFiles)
		}
		if len(b.Repositories) == 0 {
			add("%s: at least one repository is required", where)
		}
		for _, r := range b.Repositories {
			if _, ok := c.Repositories[r]; !ok {
				add("%s: unknown repository %q", where, r)
			}
		}
	}
	if c.RepoCheckSchedule != "" {
		if _, err := cron.ParseStandard(c.RepoCheckSchedule); err != nil {
			add("repo_check_schedule: %v", err)
		}
	}

	names := map[string]bool{}
	for i, u := range c.Uptime {
		where := fmt.Sprintf("uptime[%d] %q", i, u.Name)
		if u.Name == "" {
			add("uptime[%d]: name is required", i)
		} else if names[u.Name] {
			add("%s: duplicate name", where)
		}
		names[u.Name] = true
		switch u.Type {
		case CheckHTTP:
			if u.URL == "" {
				add("%s: url is required", where)
			}
		case CheckTCP:
			if u.Address == "" {
				add("%s: address is required", where)
			}
		case CheckPing:
			if u.Host == "" {
				add("%s: host is required", where)
			}
		default:
			add("%s: type must be http, tcp or ping", where)
		}
	}

	if t := c.Notifiers.Telegram; t != nil && (t.Token == "" || t.ChatID == "") {
		add("notifiers.telegram: token and chat_id are required")
	}
	if s := c.Notifiers.SMTP; s != nil && (s.Host == "" || len(s.To) == 0) {
		add("notifiers.smtp: host and to are required")
	}
	if d := c.Notifiers.Discord; d != nil && d.Webhook == "" {
		add("notifiers.discord: webhook is required")
	}
	if s := c.Notifiers.Slack; s != nil && s.Webhook == "" {
		add("notifiers.slack: webhook is required")
	}
	if ct := c.Central; ct != nil {
		if !strings.HasPrefix(ct.URL, "http://") && !strings.HasPrefix(ct.URL, "https://") {
			add("central.url must start with http:// or https://")
		}
		if ct.Token == "" {
			add("central.token is required")
		}
	}
	return errors.Join(errs...)
}

// FindBackup returns the backup job with the given name.
func (c *Config) FindBackup(name string) (*Backup, error) {
	for i := range c.Backups {
		if c.Backups[i].Name == name {
			return &c.Backups[i], nil
		}
	}
	return nil, fmt.Errorf("backup job %q not found", name)
}
