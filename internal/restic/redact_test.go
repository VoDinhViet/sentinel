package restic

import (
	"strings"
	"testing"
)

func TestRedactHidesCredentialsInBackendMessages(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			"nothing to hide",
			"unable to open repository at /var/backups/repo: no such file",
			"unable to open repository at /var/backups/repo: no such file",
		},
		{
			"password in a url",
			`Get "https://backup:s3cret@rest.example.com/repo/config": dial tcp: connection refused`,
			`Get "https://***@rest.example.com/repo/config": dial tcp: connection refused`,
		},
		{
			"user without a password in a url",
			"open sftp://backup@host:22/srv: refused",
			"open sftp://***@host:22/srv: refused",
		},
		{
			"rclone connection string",
			"rclone: :s3,provider=AWS,access_key_id=AKIAEXAMPLE,secret_access_key=wJalr/XUtnFEMI+K7MDENG=:bucket/path: 403",
			"rclone: :s3,provider=AWS,access_key_id=***,secret_access_key=***:bucket/path: 403",
		},
		{
			"quoted rclone value with a comma",
			"backend :sftp,host=h,pass='a,b c':dir failed",
			"backend :sftp,host=h,pass=***:dir failed",
		},
		{
			"token in a query string",
			"GET https://api.example.com/v1?token=abc123&region=eu: 401",
			"GET https://api.example.com/v1?token=***&region=eu: 401",
		},
		{
			"upper case names",
			"RESTIC_PASSWORD=hunter2 was rejected",
			"RESTIC_PASSWORD=*** was rejected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redact(tt.text); got != tt.want {
				t.Errorf("redact(%q)\n got %q\nwant %q", tt.text, got, tt.want)
			}
		})
	}
}

// A file name in a backed-up folder is part of restic's per-file error, and any user can name a file.
func TestErrorMessageStripsEscapeSequences(t *testing.T) {
	stdout := `{"message_type":"error","error":{"message":"read /srv/\u001b]52;c;ZXZpbA==\u0007x\u001b[2K: permission denied"},"item":"/srv/x"}` + "\n"
	got := errorMessage([]byte(stdout), nil)
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("errorMessage = %q, want no escape sequence", got)
	}
	if !strings.Contains(got, "permission denied") {
		t.Errorf("errorMessage = %q lost the reason", got)
	}
}

// The text of a failed run is stored, logged, shown by `sentinel status` and sent in alerts.
func TestErrorMessageRedactsEveryKindOfOutput(t *testing.T) {
	secretLine := `Fatal: unable to open repository at rclone::s3,secret_access_key=TOPSECRET:bucket: 403`
	tests := []struct {
		name           string
		stdout, stderr string
	}{
		{"last stderr line", "", "using parent snapshot abc\n" + secretLine + "\n"},
		{"json exit_error", `{"message_type":"exit_error","code":1,"message":"` + secretLine + `"}` + "\n", ""},
		{"json item errors", `{"message_type":"error","error":{"message":"read secret_access_key=TOPSECRET failed"},"item":"/x"}` + "\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := errorMessage([]byte(tt.stdout), []byte(tt.stderr))
			if strings.Contains(got, "TOPSECRET") {
				t.Errorf("errorMessage leaks the secret: %q", got)
			}
			if !strings.Contains(got, "secret_access_key=***") {
				t.Errorf("errorMessage = %q, want the name kept and the value masked", got)
			}
		})
	}
}
