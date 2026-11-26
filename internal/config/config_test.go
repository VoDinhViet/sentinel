package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `
server_name: web-01
store: /tmp/state.db
notifiers:
  telegram: { token: "${TG_TOKEN}", chat_id: "-100123" }
repositories:
  s3main: { url: "s3:s3.amazonaws.com/bucket/web-01", password: "${RESTIC_PW}" }
jobs:
  - name: appdb
    type: postgres
    schedule: "0 2 * * *"
    postgres: { database: app }
    repositories: [s3main]
    retention: { daily: 7, weekly: 4 }
  - name: uploads
    type: files
    schedule: "30 2 * * *"
    paths: [/var/www/uploads]
    repositories: [s3main]
verify_schedule: "0 4 * * 0"
uptime:
  - { name: website, type: http, url: "https://example.com" }
  - { name: db, type: tcp, address: "10.0.0.5:5432", fail_after: 5 }
`

func TestParseValid(t *testing.T) {
	t.Setenv("TG_TOKEN", "tok")
	t.Setenv("RESTIC_PW", "secret")

	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.Notifiers.Telegram.Token != "tok" || c.Repositories["s3main"].Password != "secret" {
		t.Errorf("env not expanded: %+v", c)
	}
	pg := c.Jobs[0].Postgres
	if pg.Host != "localhost" || pg.Port != 5432 || pg.User != "postgres" {
		t.Errorf("postgres defaults not applied: %+v", pg)
	}
	if c.Jobs[0].Timeout != 6*time.Hour {
		t.Errorf("backup timeout default = %v", c.Jobs[0].Timeout)
	}
	if c.Alert.Cooldown != time.Hour {
		t.Errorf("cooldown default = %v", c.Alert.Cooldown)
	}
	if c.ShutdownWait != 2*time.Hour {
		t.Errorf("shutdown_wait default = %v", c.ShutdownWait)
	}
	web := c.UptimeChecks[0]
	if web.Interval != time.Minute || web.Timeout != 10*time.Second || web.FailAfter != 3 || web.ExpectStatus != 200 {
		t.Errorf("uptime defaults not applied: %+v", web)
	}
	if c.UptimeChecks[1].FailAfter != 5 {
		t.Errorf("explicit fail_after overwritten: %d", c.UptimeChecks[1].FailAfter)
	}
}

func TestParseMissingEnv(t *testing.T) {
	t.Setenv("RESTIC_PW", "secret")
	_, err := Parse([]byte(validYAML)) // TG_TOKEN unset
	if err == nil || !strings.Contains(err.Error(), "TG_TOKEN") {
		t.Fatalf("want error naming TG_TOKEN, got %v", err)
	}
}

func TestParseInvalid(t *testing.T) {
	cases := map[string]struct{ yaml, want string }{
		"unknown repo": {`
repositories: {}
jobs:
  - { name: a, type: files, paths: [/x], repositories: [nope] }`, `unknown repository "nope"`},
		"bad schedule": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: files, schedule: "every day", paths: [/x], repositories: [r] }`, "invalid schedule"},
		"bad type": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: oracle, repositories: [r] }`, "type must be"},
		"job name with comma": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: "a,b", type: files, paths: [/x], repositories: [r] }`, "must not contain commas"},
		"mysql without database": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: mysql, repositories: [r] }`, "mysql is required when type is mysql"},
		"mysql bad tls": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: mysql, mysql: { database: d, tls: maybe }, repositories: [r] }`, "mysql.tls must be"},
		"mongodb without database": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: mongodb, repositories: [r] }`, "mongodb is required when type is mongodb"},
		"mongodb password without user": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: mongodb, mongodb: { database: d, password: x }, repositories: [r] }`, "mongodb.user is required"},
		"duplicate backup": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: files, paths: [/x], repositories: [r] }
  - { name: a, type: files, paths: [/y], repositories: [r] }`, "duplicate name"},
		"files without paths": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: files, repositories: [r] }`, "paths is required"},
		"http without url": {`
uptime:
  - { name: w, type: http }`, "url is required"},
		"unknown field": {`
servername: x`, "field servername not found"},
		"removed monitor section": {`
monitor: { thresholds: { cpu: 90%/5m } }`, "field monitor not found"},
		"old backups key": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - name: a`, "field backups not found"},
		"old repo_check_schedule key": {`
repo_check_schedule: daily 02:00`, "field repo_check_schedule not found"},
		"repo without password": {`
repositories: { r: { url: "/tmp/r" } }`, "password is required"},
		"negative timeout": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: files, paths: [/x], repositories: [r], timeout: -1h }`, "timeout must not be negative"},
		"negative retention": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: files, paths: [/x], repositories: [r], retention: { last: 3, weekly: -1 } }`, "retention.weekly must not be negative"},
		"only negative retention": {`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: a, type: files, paths: [/x], repositories: [r], retention: { daily: -7 } }`, "retention.daily must not be negative"},
		"disk threshold too high": {`
disk: { min_free_percent: 95 }`, "disk: min_free_percent must be at most 90"},
		"disk threshold negative": {`
disk: { min_free_percent: -5 }`, "disk: min_free_percent must not be negative"},
		"disk path relative": {`
disk: { paths: [data] }`, "disk: paths[0] must be an absolute path"},
		"heartbeat over http": {`
heartbeat: { url: "http://hc.example.com/ping/x" }`, "heartbeat: url must start with https://"},
		"api without a token": {`
api: { listen: "127.0.0.1:9311" }`, "api: token is required"},
		"api with a short token": {`
api: { listen: "127.0.0.1:9311", token: short }`, "api: token must be at least 16 characters"},
		"api listen without a port": {`
api: { listen: "127.0.0.1", token: "0123456789abcdef" }`, "api: listen must be host:port"},
		"heartbeat too often": {`
heartbeat: { url: "https://hc.example.com/ping/x", every: 10s }`, "heartbeat: every must be at least 1m"},
		"negative default retention": {`
repositories: { r: { url: "/tmp/r", password: p } }
defaults: { retention: { monthly: -2 } }`, "defaults.retention.monthly must not be negative"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestFindBackup(t *testing.T) {
	c := &Config{Jobs: []Job{{Name: "a"}, {Name: "b"}}}
	b, err := c.FindJob("b")
	if err != nil || b.Name != "b" {
		t.Fatalf("got %v, %v", b, err)
	}
	if _, err := c.FindJob("zzz"); err == nil {
		t.Fatal("want error for missing job")
	}
}

func TestEnvOnlyInValues(t *testing.T) {
	// A secret full of YAML syntax stays a plain string; comments are ignored.
	t.Setenv("RESTIC_PW", "p: [x], #y")
	c, err := Parse([]byte(`
# ${NOT_SET} in a comment is fine
repositories:
  r:
    url: /tmp/r
    password: ${RESTIC_PW}
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Repositories["r"].Password; got != "p: [x], #y" {
		t.Errorf("password = %q", got)
	}
}

func TestParseEmpty(t *testing.T) {
	c, err := Parse(nil)
	if err != nil || c.StorePath == "" {
		t.Fatalf("empty config: %+v, %v", c, err)
	}
}

func TestParseWithoutEnv(t *testing.T) {
	// The config editor reads files whose secrets it never sees.
	raw := []byte(`
repositories:
  r:
    url: s3:s3.amazonaws.com/b
    password: ${RESTIC_PW_NOT_SET}
jobs:
  - { name: a, type: files, paths: [/x], repositories: [r] }
`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("Parse should reject the unset variable")
	}
	c, err := ParseWith(raw, ParseOptions{ExpandEnv: false})
	if err != nil {
		t.Fatal(err)
	}
	if c.Repositories["r"].Password != "${RESTIC_PW_NOT_SET}" {
		t.Errorf("reference should be kept verbatim, got %q", c.Repositories["r"].Password)
	}
}

func TestRetentionString(t *testing.T) {
	if got := (Retention{}).String(); got != "keep all" {
		t.Errorf("empty = %q", got)
	}
	if got := (Retention{Last: 7, Daily: 14, Yearly: 2}).String(); got != "last 7 · daily 14 · yearly 2" {
		t.Errorf("got %q", got)
	}
}

func TestDatabaseEngineDefaults(t *testing.T) {
	c, err := Parse([]byte(`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: m, type: mysql, mysql: { database: shop }, repositories: [r] }
  - { name: g, type: mongodb, mongodb: { database: crm, user: app }, repositories: [r] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Jobs[0].MySQL; m.Host != "localhost" || m.Port != 3306 || m.User != "root" {
		t.Errorf("mysql defaults = %+v", m)
	}
	if g := c.Jobs[1].MongoDB; g.Host != "localhost" || g.Port != 27017 || g.AuthDatabase != "admin" {
		t.Errorf("mongodb defaults = %+v", g)
	}
}

func TestParseAcceptsPlainSchedules(t *testing.T) {
	const template = `
repositories: { r: { url: /tmp/r, password: p } }
jobs:
  - { name: a, type: files, paths: [/a], repositories: [r], schedule: "%s" }
verify_schedule: "%s"
`
	for _, tc := range []struct{ job, check string }{
		{"daily 02:00", "weekly sun 04:00"},
		{"manual", "manual"},
		{"every 6h", "monthly 1 04:00"},
		{"hourly :15", "0 4 * * 0"},
	} {
		if _, err := Parse([]byte(fmt.Sprintf(template, tc.job, tc.check))); err != nil {
			t.Errorf("job %q, check %q: %v", tc.job, tc.check, err)
		}
	}
	_, err := Parse([]byte(fmt.Sprintf(template, "daily 25:00", "manual")))
	if err == nil || !strings.Contains(err.Error(), "invalid schedule") {
		t.Errorf("daily 25:00: %v", err)
	}
	_, err = Parse([]byte(fmt.Sprintf(template, "manual", "weekly someday 01:00")))
	if err == nil || !strings.Contains(err.Error(), "verify_schedule") {
		t.Errorf("verify_schedule: %v", err)
	}
}

func TestDefaultsFillWhatAJobLeavesOut(t *testing.T) {
	c, err := Parse([]byte(`
repositories:
  local: { url: /tmp/r, password: p }
  offsite: { url: /tmp/o, password: p }
defaults:
  schedule: daily 02:00
  repositories: [local, offsite]
  retention: { daily: 7, weekly: 4 }
jobs:
  - { name: plain, type: files, paths: [/a] }
  - { name: own, type: files, paths: [/b], schedule: weekly sun 03:00, repositories: [offsite], retention: { last: 3 } }
  - { name: byhand, type: files, paths: [/c], schedule: manual }
`))
	if err != nil {
		t.Fatal(err)
	}
	plain, own, byHand := c.Jobs[0], c.Jobs[1], c.Jobs[2]
	if plain.Schedule != "daily 02:00" || strings.Join(plain.Repositories, ",") != "local,offsite" || plain.Retention.Daily != 7 || plain.Retention.Weekly != 4 {
		t.Errorf("plain job did not inherit: %+v", plain)
	}
	if want := (Inherited{Schedule: true, Repositories: true, Retention: true}); plain.Inherited != want {
		t.Errorf("plain.Inherited = %+v", plain.Inherited)
	}
	if own.Schedule != "weekly sun 03:00" || strings.Join(own.Repositories, ",") != "offsite" || own.Retention.Last != 3 || own.Retention.Daily != 0 {
		t.Errorf("a job's own values must win: %+v", own)
	}
	if own.Inherited != (Inherited{}) {
		t.Errorf("own.Inherited = %+v", own.Inherited)
	}
	if byHand.Schedule != "manual" {
		t.Errorf("an explicit manual schedule is kept: %q", byHand.Schedule)
	}
}

func TestOnlyRepositoryIsTheDefaultOne(t *testing.T) {
	c, err := Parse([]byte(`
repositories: { only: { url: /tmp/r, password: p } }
jobs:
  - { name: a, type: files, paths: [/a] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Jobs[0].Repositories; len(got) != 1 || got[0] != "only" || !c.Jobs[0].Inherited.Repositories {
		t.Errorf("repositories = %v, inherited %v", got, c.Jobs[0].Inherited)
	}

	_, err = Parse([]byte(`
repositories:
  one: { url: /tmp/r, password: p }
  two: { url: /tmp/s, password: p }
jobs:
  - { name: a, type: files, paths: [/a] }
`))
	if err == nil || !strings.Contains(err.Error(), "known: one, two") {
		t.Errorf("two repositories and no default must name them: %v", err)
	}
}

// The repository check is on unless the config turns it off: an unchecked repository is a backup nobody knows is readable.
func TestVerifyScheduleDefaultsToWeekly(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"not set": {"", DefaultVerifySchedule},
		"own":     {"verify_schedule: daily 03:00", "daily 03:00"},
		"manual":  {"verify_schedule: manual", "manual"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.VerifySchedule != tc.want {
				t.Errorf("VerifySchedule = %q, want %q", cfg.VerifySchedule, tc.want)
			}
		})
	}
}

func TestDefaultsAreValidated(t *testing.T) {
	_, err := Parse([]byte(`
repositories: { r: { url: /tmp/r, password: p } }
defaults: { schedule: "daily 99:00", repositories: [nope] }
`))
	for _, want := range []string{"defaults.schedule", `defaults.repositories: unknown repository "nope"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestJobOnlyRepo(t *testing.T) {
	job := &Job{Name: "db", Repositories: []string{"s3", "local"}}
	scoped, err := job.OnlyRepo("local")
	if err != nil || len(scoped.Repositories) != 1 || scoped.Repositories[0] != "local" {
		t.Fatalf("OnlyRepo(local) = %+v, %v", scoped, err)
	}
	if len(job.Repositories) != 2 {
		t.Errorf("the original job changed: %v", job.Repositories)
	}
	if _, err := job.OnlyRepo("gdrive"); err == nil || !strings.Contains(err.Error(), `does not use repository "gdrive"`) {
		t.Errorf("unknown repository: %v", err)
	}
}

func TestLoadNotifierNeedsOnlyItsOwnVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "repositories:\n  local: {url: /r, password: \"${NOT_SET_ANYWHERE}\"}\nnotifiers:\n  telegram:\n    token: \"${TEST_TG_TOKEN}\"\n    chat_id: \"${TEST_TG_CHAT}\"\n  slack:\n    webhook: \"${TEST_SLACK_HOOK}\"\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNotifier(path, "telegram"); err == nil || !strings.Contains(err.Error(), "TEST_TG_TOKEN") || strings.Contains(err.Error(), "NOT_SET_ANYWHERE") || strings.Contains(err.Error(), "TEST_SLACK_HOOK") {
		t.Errorf("want the telegram notifier's own missing variables only, got %v", err)
	}
	t.Setenv("TEST_SLACK_HOOK", "https://hooks.example/x")
	slack, err := LoadNotifier(path, "slack")
	if err != nil || slack.Slack == nil || slack.Slack.URL != "https://hooks.example/x" || slack.Telegram != nil {
		t.Errorf("slack = %+v, err %v", slack, err)
	}
	t.Setenv("TEST_TG_TOKEN", "tok")
	t.Setenv("TEST_TG_CHAT", "-1001")
	telegram, err := LoadNotifier(path, "telegram")
	if err != nil || telegram.Telegram == nil || telegram.Telegram.Token != "tok" || telegram.Telegram.ChatID != "-1001" {
		t.Errorf("telegram = %+v, err %v", telegram, err)
	}
	if none, err := LoadNotifier(path, "discord"); err != nil || none.Discord != nil {
		t.Errorf("a kind the file lacks gives nothing: %+v, %v", none, err)
	}
	if _, err := LoadNotifier(filepath.Join(t.TempDir(), "missing.yaml"), "slack"); err == nil {
		t.Error("a missing file is an error")
	}
}

func TestResourcesDefaultToCarefulAndAreValidated(t *testing.T) {
	const base = `
repositories: { r: { url: /tmp/r, password: p } }
jobs:
  - { name: a, type: files, paths: [/a], repositories: [r] }
`
	c, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if c.Resources != (Resources{Priority: PriorityLow, MaxParallel: 1}) {
		t.Errorf("resource defaults = %+v", c.Resources)
	}
	c, err = Parse([]byte(base + "resources: { priority: normal, max_parallel: 3, upload_limit_kib: 20480 }\nshutdown_wait: 30m\n"))
	if err != nil || c.Resources != (Resources{Priority: PriorityNormal, MaxParallel: 3, UploadLimitKiB: 20480}) || c.ShutdownWait != 30*time.Minute {
		t.Errorf("explicit resources = %+v, wait %v, %v", c.Resources, c.ShutdownWait, err)
	}
	for _, bad := range []string{"resources: { priority: high }", "resources: { max_parallel: -1 }", "resources: { upload_limit_kib: -5 }", "shutdown_wait: -1h"} {
		if _, err := Parse([]byte(base + bad + "\n")); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestDiskAlertIsOnUnlessZero(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml string
		want int
	}{
		"not set": {"", DefaultMinFreePercent},
		"own":     {"disk: { min_free_percent: 20 }", 20},
		"off":     {"disk: { min_free_percent: 0 }", 0},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Disk.MinFree(); got != tc.want {
				t.Errorf("MinFree = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestHeartbeatIsOffWithoutAURLAndDefaultsToFiveMinutes(t *testing.T) {
	t.Setenv("HC_URL", "https://hc-ping.com/abc")
	cfg, err := Parse([]byte("heartbeat: { url: '${HC_URL}' }"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Heartbeat.URL != "https://hc-ping.com/abc" || cfg.Heartbeat.Every != DefaultHeartbeatEvery {
		t.Errorf("heartbeat = %+v", cfg.Heartbeat)
	}
	cfg, err = Parse([]byte(""))
	if err != nil || cfg.Heartbeat.URL != "" || cfg.Heartbeat.Every != 0 {
		t.Errorf("heartbeat without a url = %+v, %v; want it off", cfg.Heartbeat, err)
	}
}

func TestAPIIsOffUnlessItListens(t *testing.T) {
	cfg, err := Parse([]byte("server_name: x"))
	if err != nil || cfg.API.Listen != "" {
		t.Errorf("api = %+v, %v; want it off", cfg.API, err)
	}
	cfg, err = Parse([]byte("api: { listen: '127.0.0.1:9311', token: '0123456789abcdef' }"))
	if err != nil || cfg.API.Listen != "127.0.0.1:9311" || !cfg.API.Loopback() {
		t.Errorf("api = %+v, %v", cfg.API, err)
	}
}

func TestAPILoopback(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:9311": true, "[::1]:9311": true, "localhost:9311": true, "127.5.5.5:1": true,
		"0.0.0.0:9311": false, ":9311": false, "[::]:9311": false, "10.0.0.5:9311": false, "example.com:9311": false,
		"": false, "no-port": false,
	}
	for listen, want := range tests {
		if got := (API{Listen: listen}).Loopback(); got != want {
			t.Errorf("Loopback(%q) = %v, want %v", listen, got, want)
		}
	}
}

func TestAPITokenIsCheckedAfterTheVariableIsExpanded(t *testing.T) {
	t.Setenv("API_TOKEN", "short")
	if _, err := Parse([]byte("api: { listen: '127.0.0.1:9311', token: '${API_TOKEN}' }")); err == nil || !strings.Contains(err.Error(), "at least 16 characters") {
		t.Errorf("a short value must be refused: %v", err)
	}
	t.Setenv("API_TOKEN", "short${x}")
	if _, err := Parse([]byte("api: { listen: '127.0.0.1:9311', token: '${API_TOKEN}' }")); err == nil {
		t.Error("a short value that contains ${ must be refused")
	}
	t.Setenv("API_TOKEN", "0123456789abcdef")
	if _, err := Parse([]byte("api: { listen: '127.0.0.1:9311', token: '${API_TOKEN}' }")); err != nil {
		t.Errorf("a long value is accepted: %v", err)
	}
}
