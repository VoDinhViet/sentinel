package config

import (
	"strings"
	"testing"
)

const notifierBase = "repositories:\n  local: {url: /r, password: pw}\nnotifiers:\n"

// A Discord or Slack webhook URL is itself the secret: over http it can be read and used by anyone on the path.
func TestWebhooksMustUseHTTPS(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		expand  bool
		env     map[string]string
		wantErr string
	}{
		{"discord over http", "  discord: {webhook: \"http://discord.example/api/webhooks/1/x\"}\n", false, nil, "notifiers.discord: webhook must start with https://"},
		{"slack over http", "  slack: {webhook: \"http://hooks.slack.example/x\"}\n", false, nil, "notifiers.slack: webhook must start with https://"},
		{"discord over https", "  discord: {webhook: \"https://discord.com/api/webhooks/1/x\"}\n", false, nil, ""},
		{"a reference is checked once it has a value", "  slack: {webhook: \"${SLACK_HOOK}\"}\n", false, nil, ""},
		{"a reference that expands to http", "  slack: {webhook: \"${SLACK_HOOK}\"}\n", true, map[string]string{"SLACK_HOOK": "http://hooks.slack.example/x"}, "notifiers.slack: webhook must start with https://"},
		{"a reference that expands to https", "  slack: {webhook: \"${SLACK_HOOK}\"}\n", true, map[string]string{"SLACK_HOOK": "https://hooks.slack.com/x"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			_, err := ParseWith([]byte(notifierBase+tt.yaml), ParseOptions{ExpandEnv: tt.expand})
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("err = %v, want it accepted", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestSMTPInsecurePlaintextIsOffUnlessSet(t *testing.T) {
	cfg, err := ParseWith([]byte(notifierBase+"  smtp: {host: smtp.example.com, port: 587, to: [ops@example.com]}\n"), ParseOptions{})
	if err != nil || cfg.Notifiers.SMTP.InsecurePlaintext {
		t.Fatalf("default: %+v, %v", cfg, err)
	}
	cfg, err = ParseWith([]byte(notifierBase+"  smtp: {host: relay.internal, port: 25, to: [ops@example.com], insecure_plaintext: true}\n"), ParseOptions{})
	if err != nil || !cfg.Notifiers.SMTP.InsecurePlaintext {
		t.Fatalf("set: %+v, %v", cfg, err)
	}
}
