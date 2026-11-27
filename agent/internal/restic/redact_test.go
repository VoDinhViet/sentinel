package restic

import (
	"strings"
	"testing"
)

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
