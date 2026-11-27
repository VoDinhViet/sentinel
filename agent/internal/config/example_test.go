package config

import (
	"os"
	"strings"
	"testing"
)

// The shipped example must always be a valid config.
func TestExampleConfigParses(t *testing.T) {
	for _, v := range []string{"TG_TOKEN", "SMTP_USER", "SMTP_PASS",
		"RESTIC_PASSWORD", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "PGPASSWORD"} {
		t.Setenv(v, "x")
	}
	t.Setenv("DISCORD_WEBHOOK", "https://discord.com/api/webhooks/1/x") // a webhook must be https
	t.Setenv("SLACK_WEBHOOK", "https://hooks.slack.com/services/x")
	raw, err := os.ReadFile("../../../deploy/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Jobs) != 2 || len(c.UptimeChecks) != 3 {
		t.Errorf("example not fully parsed: %+v", c)
	}
	appdb, uploads := c.Jobs[0], c.Jobs[1]
	if appdb.Schedule != "daily 02:00" || len(appdb.Repositories) != 2 || appdb.Retention.Monthly != 6 {
		t.Errorf("appdb must take the defaults: %+v", appdb)
	}
	if uploads.Schedule != "daily 02:30" || len(uploads.Repositories) != 1 || uploads.Retention.Monthly != 0 {
		t.Errorf("uploads keeps its own values: %+v", uploads)
	}
}

// The per-user example must parse too, and must not need any secret in the file.
func TestUserExampleConfigParses(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	t.Setenv("RESTIC_PASSWORD", "x")
	raw, err := os.ReadFile("../../../deploy/config.user.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.StorePath != "/home/someone/.local/state/sentinel/state.db" || len(c.Jobs) != 1 || c.Jobs[0].Paths[0] != "/home/someone/Documents" {
		t.Errorf("example not fully parsed: %+v", c)
	}
	if documents := c.Jobs[0]; documents.Schedule != "daily 02:00" || len(documents.Repositories) != 1 || documents.Retention.Daily != 7 {
		t.Errorf("documents must take the defaults and the only repository: %+v", documents)
	}
	if c.Repositories["local"].Password != "x" || !strings.Contains(string(raw), "${RESTIC_PASSWORD}") {
		t.Error("the repository password must come from RESTIC_PASSWORD, not from the file")
	}
}
