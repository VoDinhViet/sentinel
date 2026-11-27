package config

import (
	"strings"
	"testing"
)

// One run reports every broken field, each with the job it belongs to, so the file is fixed in one pass.
func TestParseReportsEveryFieldProblem(t *testing.T) {
	_, err := Parse([]byte(`
repositories: { r: { url: "/tmp/r", password: p } }
jobs:
  - { name: db, type: mysql, mysql: { tls: maybe }, repositories: [r] }
  - { name: web, type: files, paths: [/x], repositories: [r], timeout: -1h }
notifiers:
  discord: { webhook: "http://discord.example/x" }
heartbeat: { url: "https://hc.example.com/x", every: 10s }
`))
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{
		`jobs[0] "db": mysql.database is required`,
		`jobs[0] "db": mysql.tls must be disabled or required`,
		`jobs[1] "web": timeout must not be negative`,
		"notifiers.discord: webhook must start with https://",
		"heartbeat: every must be at least 1m",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}
