package config

import (
	"os"
	"strings"
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
	if len(c.Backups) != 2 || len(c.Uptime) != 3 {
		t.Errorf("example not fully parsed: %+v", c)
	}
}

// The per-user example must parse too, and must not need any secret in the file.
func TestUserExampleConfigParses(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	t.Setenv("RESTIC_PASSWORD", "x")
	raw, err := os.ReadFile("../../deploy/config.user.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Store != "/home/someone/.local/state/sentinel/state.db" || len(c.Backups) != 1 || c.Backups[0].Paths[0] != "/home/someone/Documents" {
		t.Errorf("example not fully parsed: %+v", c)
	}
	if c.Repositories["local"].Password != "x" || !strings.Contains(string(raw), "${RESTIC_PASSWORD}") {
		t.Error("the repository password must come from RESTIC_PASSWORD, not from the file")
	}
}
