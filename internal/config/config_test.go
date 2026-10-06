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
	cpu := c.Monitor.Thresholds.CPU
	if cpu.Value != 90 || cpu.For != 5*time.Minute {
		t.Errorf("cpu threshold = %+v", cpu)
	}
	if c.Monitor.Thresholds.Memory != nil {
		t.Errorf("unset threshold should be nil")
	}
	if c.Monitor.Interval != time.Minute {
		t.Errorf("monitor interval default = %v", c.Monitor.Interval)
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
  - { name: a, type: mysql, repositories: [r] }`, "type must be"},
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
		"bad threshold": {`
monitor: { thresholds: { cpu: "ninety" } }`, "bad value"},
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

func TestParseThreshold(t *testing.T) {
	cases := []struct {
		in      string
		want    Threshold
		wantErr bool
	}{
		{"85%", Threshold{Value: 85}, false},
		{"90%/5m", Threshold{Value: 90, For: 5 * time.Minute}, false},
		{"2.0/30s", Threshold{Value: 2, For: 30 * time.Second}, false},
		{" 75 % / 1m ", Threshold{}, true},
		{"0%", Threshold{}, true},
		{"90%/soon", Threshold{}, true},
		{"", Threshold{}, true},
	}
	for _, tc := range cases {
		got, err := ParseThreshold(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got %+v", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q: got %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
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
