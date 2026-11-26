package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"sentinel/internal/schedule"
)

func (c *Config) validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	for name, repo := range c.Repositories {
		if repo.URL == "" {
			add("repository %q: url is required", name)
		}
		if repo.Password == "" {
			add("repository %q: password is required", name)
		}
		for _, varName := range slices.Sorted(maps.Keys(repo.Env)) {
			switch {
			case !envName.MatchString(varName):
				add("repository %q: env %q is not a variable name", name, varName)
			case IsDangerousEnvName(varName):
				add("repository %q: env %s cannot be set: it changes which programs restic runs", name, varName)
			}
		}
	}

	seenJobs := map[string]bool{}
	for i, job := range c.Jobs {
		where := fmt.Sprintf("jobs[%d] %q", i, job.Name)
		if job.Name == "" {
			add("jobs[%d]: name is required", i)
		} else if strings.ContainsAny(job.Name, ",\x00\n\r") {
			// restic reads a comma in --tag as "all of these tags", so a job
			// named "a,b" would never match its own snapshots.
			add("%s: name must not contain commas or line breaks", where)
		} else if seenJobs[job.Name] {
			add("%s: duplicate name", where)
		}
		seenJobs[job.Name] = true
		if !schedule.IsManual(job.Schedule) {
			if _, err := schedule.Parse(job.Schedule); err != nil {
				add("%s: invalid schedule: %v", where, err)
			}
		}
		switch job.Type {
		case JobPostgres:
			if job.Postgres == nil || job.Postgres.Database == "" {
				add("%s: postgres.database is required", where)
			}
		case JobMySQL:
			if job.MySQL == nil || job.MySQL.Database == "" {
				add("%s: mysql.database is required", where)
			} else if tls := job.MySQL.TLS; tls != "" && tls != "disabled" && tls != "required" {
				add("%s: mysql.tls must be \"disabled\" or \"required\"", where)
			}
		case JobMongoDB:
			if job.MongoDB == nil || job.MongoDB.Database == "" {
				add("%s: mongodb.database is required", where)
			} else if job.MongoDB.Password != "" && job.MongoDB.User == "" {
				add("%s: mongodb.user is required when a password is set", where)
			}
		case JobFiles:
			if len(job.Paths) == 0 {
				add("%s: paths is required", where)
			}
		default:
			add("%s: type must be %q, %q, %q or %q", where, JobPostgres, JobMySQL, JobMongoDB, JobFiles)
		}
		if job.Timeout < 0 {
			add("%s: timeout must not be negative", where)
		}
		if rule := job.Retention.negativeRule(); rule != "" && !job.Inherited.Retention {
			add("%s: retention.%s must not be negative", where, rule)
		}
		if len(job.Repositories) == 0 {
			add("%s: at least one repository is required: list repositories: on the job or set defaults.repositories (known: %s)",
				where, strings.Join(slices.Sorted(maps.Keys(c.Repositories)), ", "))
		}
		for _, repoName := range job.Repositories {
			if _, ok := c.Repositories[repoName]; !ok {
				add("%s: unknown repository %q", where, repoName)
			}
		}
	}
	if !schedule.IsManual(c.Defaults.Schedule) {
		if _, err := schedule.Parse(c.Defaults.Schedule); err != nil {
			add("defaults.schedule: %v", err)
		}
	}
	if rule := c.Defaults.Retention.negativeRule(); rule != "" {
		add("defaults.retention.%s must not be negative", rule)
	}
	for _, name := range c.Defaults.Repositories {
		if _, ok := c.Repositories[name]; !ok {
			add("defaults.repositories: unknown repository %q", name)
		}
	}
	if !schedule.IsManual(c.VerifySchedule) {
		if _, err := schedule.Parse(c.VerifySchedule); err != nil {
			add("verify_schedule: %v", err)
		}
	}

	if c.Resources.Priority != PriorityLow && c.Resources.Priority != PriorityNormal {
		add("resources.priority: must be %q or %q", PriorityLow, PriorityNormal)
	}
	if c.Resources.MaxParallel < 1 {
		add("resources.max_parallel: must be at least 1")
	}
	if c.Resources.UploadLimitKiB < 0 {
		add("resources.upload_limit_kib: must not be negative")
	}
	if c.ShutdownWait < 0 {
		add("shutdown_wait: must not be negative")
	}
	if c.Heartbeat.URL != "" {
		if problem := heartbeatProblem(c.Heartbeat); problem != "" {
			add("heartbeat: %s", problem)
		}
	}
	if c.API.Listen != "" {
		if problem := apiProblem(c.API); problem != "" {
			add("api: %s", problem)
		}
	}
	if c.Disk.MinFree() < 0 || c.Disk.MinFree() > 90 {
		add("disk.min_free_percent: must be between 0 (off) and 90")
	}
	for i, path := range c.Disk.Paths {
		if !filepath.IsAbs(path) {
			add("disk.paths[%d]: %q must be an absolute path", i, path)
		}
	}

	seenChecks := map[string]bool{}
	for i, check := range c.UptimeChecks {
		where := fmt.Sprintf("uptime[%d] %q", i, check.Name)
		if check.Name == "" {
			add("uptime[%d]: name is required", i)
		} else if seenChecks[check.Name] {
			add("%s: duplicate name", where)
		}
		seenChecks[check.Name] = true
		switch check.Type {
		case CheckHTTP:
			if check.URL == "" {
				add("%s: url is required", where)
			}
		case CheckTCP:
			if check.Address == "" {
				add("%s: address is required", where)
			}
		case CheckPing:
			if check.Host == "" {
				add("%s: host is required", where)
			}
		default:
			add("%s: type must be http, tcp or ping", where)
		}
	}

	if telegram := c.Notifiers.Telegram; telegram != nil && (telegram.Token == "" || telegram.ChatID == "") {
		add("notifiers.telegram: token and chat_id are required")
	}
	if smtp := c.Notifiers.SMTP; smtp != nil && (smtp.Host == "" || len(smtp.To) == 0) {
		add("notifiers.smtp: host and to are required")
	}
	if discord := c.Notifiers.Discord; discord != nil {
		if problem := webhookProblem(discord.URL); problem != "" {
			add("notifiers.discord: %s", problem)
		}
	}
	if slack := c.Notifiers.Slack; slack != nil {
		if problem := webhookProblem(slack.URL); problem != "" {
			add("notifiers.slack: %s", problem)
		}
	}
	return errors.Join(errs...)
}

// webhookProblem says what is wrong with a chat webhook URL, "" when nothing is. The URL is the secret that
// lets anyone post to the channel, so it must not travel over plain http. A value that is still a ${VAR}
// reference (the config editor reads files without expanding) is checked once it has a value.
func webhookProblem(webhook string) string {
	switch {
	case webhook == "":
		return "webhook is required"
	case strings.Contains(webhook, "${"):
		return ""
	case !strings.HasPrefix(webhook, "https://"):
		return "webhook must start with https://"
	}
	return ""
}

// heartbeatProblem says what is wrong with the heartbeat settings, "" when nothing is. The URL often
// carries the secret that identifies the check, so like a webhook it must not travel over plain http.
func heartbeatProblem(heartbeat Heartbeat) string {
	switch {
	case !strings.Contains(heartbeat.URL, "${") && !strings.HasPrefix(heartbeat.URL, "https://"):
		return "url must start with https://"
	case heartbeat.Every < time.Minute:
		return "every must be at least 1m"
	}
	return ""
}

// apiProblem says what is wrong with the API settings, "" when nothing is. The token is required
// even on the loopback: every user of the server can reach it there.
func apiProblem(api API) string {
	if _, _, err := net.SplitHostPort(api.Listen); err != nil {
		return "listen must be host:port, e.g. 127.0.0.1:9311"
	}
	if api.Token == "" {
		return "token is required: write token: ${SENTINEL_API_TOKEN} and put the value in the env file"
	}
	if !wholeEnvRef.MatchString(api.Token) && len(api.Token) < MinTokenLength { // an unexpanded ${VAR} is checked once it has a value
		return fmt.Sprintf("token must be at least %d characters", MinTokenLength)
	}
	return ""
}
