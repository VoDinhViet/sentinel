package uptime

import (
	"context"
	"strings"
	"testing"

	"sentinel/internal/config"
)

// A URL often carries the secret itself (user:pass@host, ?token=…), and the endpoint is printed by
// `sentinel check` and sent in the alert text to every notifier.
func TestEndpointHidesCredentialsInTheURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"plain", "https://example.com/health", "https://example.com/health"},
		{"port and path", "http://example.com:8080/a/b", "http://example.com:8080/a/b"},
		{"password in the address", "https://monitor:s3cret@example.com/health", "https://example.com/health"},
		{"user without a password", "https://monitor@example.com/health", "https://example.com/health"},
		{"token in the query", "https://example.com/ping?token=abc123&region=eu", "https://example.com/ping?token=***&region=***"},
		{"bare query", "https://example.com/ping?abc123", "https://example.com/ping?***"},
		{"fragment", "https://example.com/ping#secret", "https://example.com/ping"},
		{"everything at once", "https://u:p@example.com/h?k=v#f", "https://example.com/h?k=***"},
		{"not a URL", "http://u:p@exa mple.com/h?k=v", "(invalid URL)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := config.UptimeCheck{Type: config.CheckHTTP, URL: tt.url}
			if got := Endpoint(check); got != tt.want {
				t.Errorf("Endpoint(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestEndpointOfOtherChecksIsUnchanged(t *testing.T) {
	if got := Endpoint(config.UptimeCheck{Type: config.CheckTCP, Address: "db.internal:5432"}); got != "db.internal:5432" {
		t.Errorf("tcp endpoint = %q", got)
	}
	if got := Endpoint(config.UptimeCheck{Type: config.CheckPing, Host: "10.0.0.1"}); got != "10.0.0.1" {
		t.Errorf("ping endpoint = %q", got)
	}
}

// Go's parse error quotes the whole URL; the failure reaches the store, the log and the alert.
func TestProbeErrorOfAMalformedURLDoesNotQuoteIt(t *testing.T) {
	check := httpCheck("http://monitor:s3cret@example.com/%zz?token=hunter2")
	result := Probe(context.Background(), check)
	if result.OK {
		t.Fatal("a malformed URL must fail")
	}
	for _, secret := range []string{"s3cret", "hunter2", "monitor", "example.com"} {
		if strings.Contains(result.Error, secret) {
			t.Errorf("error %q contains %q", result.Error, secret)
		}
	}
	if !strings.Contains(result.Error, "invalid URL") {
		t.Errorf("error = %q, want it to say the URL is invalid", result.Error)
	}
}
