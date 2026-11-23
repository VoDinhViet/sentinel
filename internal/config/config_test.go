package config

import (
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
	// The server validates agent configs whose secrets it never sees.
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

func TestCentral(t *testing.T) {
	c, err := Parse([]byte("central: { url: \"https://s.example.com\", token: t, managed: true }"))
	if err != nil || !c.Central.Managed || c.RestoreDir != "/var/lib/sentinel/restore" {
		t.Fatalf("central = %+v, %v", c, err)
	}
	if _, err := Parse([]byte("central: { url: s.example.com, token: t }")); err == nil {
		t.Fatal("want error for url without scheme")
	}
}

func TestParseServer(t *testing.T) {
	c, err := ParseServer([]byte(`admin: { username: admin, password_hash: "$2a$10$abcdefghijklmnopqrstuv" }`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8443" || c.OfflineAfter != 3*time.Minute || !*c.SecureCookies {
		t.Errorf("defaults = %+v", c)
	}
	if _, err := ParseServer([]byte(`admin: { username: admin, password_hash: hunter2 }`)); err == nil {
		t.Error("plain-text password should be rejected")
	}
	if _, err := ParseServer(nil); err == nil {
		t.Error("missing admin should be rejected")
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

func TestParseServerBeszel(t *testing.T) {
	base := `admin: { username: admin, password_hash: "$2a$10$abcdefghijklmnopqrstuv" }` + "\n"
	c, err := ParseServer([]byte(base))
	if err != nil || c.Beszel != nil {
		t.Fatalf("no beszel section: %+v %v", c.Beszel, err)
	}
	t.Setenv("BESZEL_PW", "s3cret")
	c, err = ParseServer([]byte(base + "beszel: { url: \"https://beszel.example.com\", email: sentinel@example.com, password: \"${BESZEL_PW}\" }"))
	if err != nil || c.Beszel == nil || c.Beszel.Password != "s3cret" || c.Beszel.Poll != 30*time.Second {
		t.Fatalf("beszel = %+v, %v", c.Beszel, err)
	}
	for name, bad := range map[string]string{
		"url":      `beszel: { url: "beszel.example.com", email: a@b.c, password: x }`,
		"email":    `beszel: { url: "http://h", password: x }`,
		"password": `beszel: { url: "http://h", email: a@b.c }`,
		"poll":     `beszel: { url: "http://h", email: a@b.c, password: x, poll: 1s }`,
		"public":   `beszel: { url: "http://h", email: a@b.c, password: x, public_url: "hub.example.com" }`,
	} {
		if _, err := ParseServer([]byte(base + bad)); err == nil {
			t.Errorf("%s: invalid beszel section accepted", name)
		}
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
