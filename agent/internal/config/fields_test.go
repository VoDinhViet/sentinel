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
api: { listen: "127.0.0.1", token: "0123456789abcdef" }
`))
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{
		`jobs[0] "db": mysql.database is required`,
		`jobs[0] "db": mysql.tls must be disabled or required`,
		`jobs[1] "web": timeout must not be negative`,
		"notifiers.discord: webhook must start with https://",
		"api: listen must be host:port",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestListenAddrAcceptsIPv6(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:9311": true, "[::1]:9311": true, "localhost:80": true, "127.0.0.1": false, "": false} {
		if got := isListenAddr(addr); got != want {
			t.Errorf("isListenAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestAPITokenRule(t *testing.T) {
	for token, want := range map[string]bool{"0123456789abcdef": true, "${API_TOKEN}": true, "short": false, "short${X}": false} {
		if got := isAPIToken(token); got != want {
			t.Errorf("isAPIToken(%q) = %v, want %v", token, got, want)
		}
	}
}
