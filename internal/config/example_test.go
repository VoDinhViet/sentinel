package config

import (
	"os"
	"testing"
)

// The shipped example must always be a valid config.
func TestExampleConfigParses(t *testing.T) {
	for _, v := range []string{"TG_TOKEN", "SMTP_USER", "SMTP_PASS", "DISCORD_WEBHOOK", "SLACK_WEBHOOK",
		"RESTIC_PASSWORD", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "PGPASSWORD"} {
		t.Setenv(v, "x")
	}
	raw, err := os.ReadFile("../../deploy/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Backups) != 2 || len(c.Uptime) != 3 || c.Monitor == nil {
		t.Errorf("example not fully parsed: %+v", c)
	}
}
