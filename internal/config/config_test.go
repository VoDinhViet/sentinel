package config

import (
	"fmt"
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
backups:
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
repo_check_schedule: "0 4 * * 0"
monitor:
  thresholds: { cpu: 90%/5m, disk: 85%, load_per_core: 2.0/5m }
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
	pg := c.Backups[0].Postgres
	if pg.Host != "localhost" || pg.Port != 5432 || pg.User != "postgres" {
		t.Errorf("postgres defaults not applied: %+v", pg)
	}
	if c.Backups[0].Timeout != 6*time.Hour {
		t.Errorf("backup timeout default = %v", c.Backups[0].Timeout)
	}
	if c.Alert.Cooldown != time.Hour {
		t.Errorf("cooldown default = %v", c.Alert.Cooldown)
	}
	web := c.Uptime[0]
	if web.Interval != time.Minute || web.Timeout != 10*time.Second || web.FailAfter != 3 || web.ExpectStatus != 200 {
		t.Errorf("uptime defaults not applied: %+v", web)
	}
	if c.Uptime[1].FailAfter != 5 {
		t.Errorf("explicit fail_after overwritten: %d", c.Uptime[1].FailAfter)
	}
	if c.Monitor == nil {
		t.Errorf("legacy monitor section must still parse")
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
backups:
  - { name: a, type: files, paths: [/x], repositories: [nope] }`, `unknown repository "nope"`},
		"bad schedule": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - { name: a, type: files, schedule: "every day", paths: [/x], repositories: [r] }`, "invalid schedule"},
		"bad type": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - { name: a, type: oracle, repositories: [r] }`, "type must be"},
		"job name with comma": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - { name: "a,b", type: files, paths: [/x], repositories: [r] }`, "must not contain commas"},
		"mysql without database": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - { name: a, type: mysql, repositories: [r] }`, "mysql.database is required"},
		"mysql bad tls": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - { name: a, type: mysql, mysql: { database: d, tls: maybe }, repositories: [r] }`, "mysql.tls must be"},
		"mongodb without database": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - { name: a, type: mongodb, repositories: [r] }`, "mongodb.database is required"},
		"mongodb password without user": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - { name: a, type: mongodb, mongodb: { database: d, password: x }, repositories: [r] }`, "mongodb.user is required"},
		"duplicate backup": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - { name: a, type: files, paths: [/x], repositories: [r] }
  - { name: a, type: files, paths: [/y], repositories: [r] }`, "duplicate name"},
		"files without paths": {`
repositories: { r: { url: "/tmp/r", password: p } }
backups:
  - { name: a, type: files, repositories: [r] }`, "paths is required"},
		"http without url": {`
uptime:
  - { name: w, type: http }`, "url is required"},
		"unknown field": {`
servername: x`, "field servername not found"},
		"repo without password": {`
repositories: { r: { url: "/tmp/r" } }`, "password is required"},
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
	c := &Config{Backups: []Backup{{Name: "a"}, {Name: "b"}}}
	b, err := c.FindBackup("b")
	if err != nil || b.Name != "b" {
		t.Fatalf("got %v, %v", b, err)
	}
	if _, err := c.FindBackup("zzz"); err == nil {
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
	if err != nil || c.Store == "" {
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
backups:
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
backups:
  - { name: m, type: mysql, mysql: { database: shop }, repositories: [r] }
  - { name: g, type: mongodb, mongodb: { database: crm, user: app }, repositories: [r] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Backups[0].MySQL; m.Host != "localhost" || m.Port != 3306 || m.User != "root" {
		t.Errorf("mysql defaults = %+v", m)
	}
	if g := c.Backups[1].MongoDB; g.Host != "localhost" || g.Port != 27017 || g.AuthDatabase != "admin" {
		t.Errorf("mongodb defaults = %+v", g)
	}
}

func TestParseAcceptsPlainSchedules(t *testing.T) {
	const template = `
repositories: { r: { url: /tmp/r, password: p } }
backups:
  - { name: a, type: files, paths: [/a], repositories: [r], schedule: "%s" }
repo_check_schedule: "%s"
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
	if err == nil || !strings.Contains(err.Error(), "repo_check_schedule") {
		t.Errorf("repo_check_schedule: %v", err)
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
backups:
  - { name: plain, type: files, paths: [/a] }
  - { name: own, type: files, paths: [/b], schedule: weekly sun 03:00, repositories: [offsite], retention: { last: 3 } }
  - { name: byhand, type: files, paths: [/c], schedule: manual }
`))
	if err != nil {
		t.Fatal(err)
	}
	plain, own, byHand := c.Backups[0], c.Backups[1], c.Backups[2]
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
backups:
  - { name: a, type: files, paths: [/a] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Backups[0].Repositories; len(got) != 1 || got[0] != "only" || !c.Backups[0].Inherited.Repositories {
		t.Errorf("repositories = %v, inherited %v", got, c.Backups[0].Inherited)
	}

	_, err = Parse([]byte(`
repositories:
  one: { url: /tmp/r, password: p }
  two: { url: /tmp/s, password: p }
backups:
  - { name: a, type: files, paths: [/a] }
`))
	if err == nil || !strings.Contains(err.Error(), "known: one, two") {
		t.Errorf("two repositories and no default must name them: %v", err)
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
