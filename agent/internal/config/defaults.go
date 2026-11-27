package config

import (
	"maps"
	"os"
	"slices"
	"time"

	"sentinel/pkg/notify"
)

// DefaultJobTimeout is how long one run of a job may take when the job sets no timeout.
const DefaultJobTimeout = 6 * time.Hour

// DefaultVerifySchedule is when `restic check` runs on every repository when the config sets no
// verify_schedule. "manual" turns it off.
const DefaultVerifySchedule = "weekly sun 04:00"

// DefaultPruneSchedule is when the daemon runs `restic prune` on every repository when the config sets no
// prune_schedule. "manual" turns it off. Pruning reads the whole repository and locks it, so it runs once
// a week, not after every backup; a day apart from the check so that the two do not wait for each other.
const DefaultPruneSchedule = "weekly sat 03:00"

func (c *Config) applyDefaults() {
	if c.ServerName == "" {
		c.ServerName, _ = os.Hostname()
	}
	if c.StorePath == "" {
		c.StorePath = "/var/lib/sentinel/state.db"
	}
	if c.Resources.Priority == "" {
		c.Resources.Priority = PriorityLow
	}
	if c.Resources.MaxParallel == 0 {
		c.Resources.MaxParallel = 1
	}
	if c.VerifySchedule == "" {
		c.VerifySchedule = DefaultVerifySchedule
	}
	if c.PruneSchedule == "" {
		c.PruneSchedule = DefaultPruneSchedule
	}
	if c.Heartbeat.URL != "" && c.Heartbeat.Every == 0 {
		c.Heartbeat.Every = notify.DefaultHeartbeatEvery
	}
	if c.ShutdownWait == 0 {
		c.ShutdownWait = 2 * time.Hour
	}
	if c.Alert.Cooldown == 0 {
		c.Alert.Cooldown = time.Hour
	}
	for i := range c.Jobs {
		job := &c.Jobs[i]
		if job.Timeout == 0 {
			job.Timeout = DefaultJobTimeout
		}
		c.inheritDefaults(job)
		ApplyDatabaseDefaults(job)
	}
	for i := range c.UptimeChecks {
		check := &c.UptimeChecks[i]
		if check.Interval == 0 {
			check.Interval = time.Minute
		}
		if check.Timeout == 0 {
			check.Timeout = 10 * time.Second
		}
		if check.FailAfter == 0 {
			check.FailAfter = 3
		}
		if check.Type == CheckHTTP && check.ExpectStatus == 0 {
			check.ExpectStatus = 200
		}
	}
	if smtp := c.Notifiers.SMTP; smtp != nil {
		if smtp.Port == 0 {
			smtp.Port = 587
		}
		if smtp.From == "" {
			smtp.From = smtp.User
		}
	}
}

// inheritDefaults fills what a job leaves out from the defaults block; a job without
// repositories uses the only repository when the config has just one.
// ApplyDatabaseDefaults fills in what a database job leaves out: the host, the port of its
// engine and the user (postgres or root; a mongodb user authenticates against admin).
func ApplyDatabaseDefaults(job *Job) {
	if pg := job.Postgres; pg != nil {
		if pg.Host == "" {
			pg.Host = "localhost"
		}
		if pg.Port == 0 {
			pg.Port = 5432
		}
		if pg.User == "" {
			pg.User = "postgres"
		}
	}
	if my := job.MySQL; my != nil {
		if my.Host == "" {
			my.Host = "localhost"
		}
		if my.Port == 0 {
			my.Port = 3306
		}
		if my.User == "" {
			my.User = "root"
		}
	}
	if mongo := job.MongoDB; mongo != nil {
		if mongo.Host == "" {
			mongo.Host = "localhost"
		}
		if mongo.Port == 0 {
			mongo.Port = 27017
		}
		if mongo.User != "" && mongo.AuthDatabase == "" {
			mongo.AuthDatabase = "admin"
		}
	}
}

func (c *Config) inheritDefaults(job *Job) {
	if job.Schedule == "" && c.Defaults.Schedule != "" {
		job.Schedule, job.Inherited.Schedule = c.Defaults.Schedule, true
	}
	if len(job.Repositories) == 0 {
		switch {
		case len(c.Defaults.Repositories) > 0:
			job.Repositories, job.Inherited.Repositories = slices.Clone(c.Defaults.Repositories), true
		case len(c.Repositories) == 1:
			job.Repositories, job.Inherited.Repositories = slices.Collect(maps.Keys(c.Repositories)), true
		}
	}
	if job.Retention.Empty() && !c.Defaults.Retention.Empty() {
		job.Retention, job.Inherited.Retention = c.Defaults.Retention, true
	}
}
