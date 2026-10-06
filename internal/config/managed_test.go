package config

import (
	"strings"
	"testing"
)

const localFile = `
server_name: web-01
store: /tmp/s.db
central: { url: "https://s.example.com", token: "${TOK}", managed: true }
# keep me
repositories:
  r: { url: /tmp/r, password: "${PW}" }
backups:
  - { name: a, type: files, paths: [/x], repositories: [r] }
`

func TestStripLocal(t *testing.T) {
	out, err := StripLocal([]byte(localFile))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, k := range LocalOnlyKeys {
		if strings.Contains(s, k+":") {
			t.Errorf("%s not stripped:\n%s", k, s)
		}
	}
	if !strings.Contains(s, "# keep me") || !strings.Contains(s, "${PW}") {
		t.Errorf("comments or env references lost:\n%s", s)
	}
	if err := CheckManaged(out); err != nil {
		t.Errorf("stripped config rejected: %v", err)
	}
	if err := CheckManaged([]byte(localFile)); err == nil {
		t.Error("CheckManaged accepted local-only keys")
	}
}

func TestMergeManaged(t *testing.T) {
	t.Setenv("TOK", "t")
	t.Setenv("PW", "p")
	managed := []byte(`
repositories:
  r: { url: /tmp/other, password: "${PW}" }
uptime:
  - { name: web, type: http, url: "https://example.com" }
`)
	merged, err := MergeManaged([]byte(localFile), managed)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(merged)
	if err != nil {
		t.Fatalf("merged config invalid: %v\n%s", err, merged)
	}
	if c.ServerName != "web-01" || c.Central == nil || c.Repositories["r"].URL != "/tmp/other" ||
		len(c.Uptime) != 1 || len(c.Backups) != 0 {
		t.Errorf("merged = %+v", c)
	}
	if _, err := MergeManaged([]byte(localFile), []byte("store: /evil.db")); err == nil {
		t.Error("managed config may not override store")
	}
}
