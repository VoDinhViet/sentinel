package redact

import "testing"

func TestTextHidesCredentialsInBackendMessages(t *testing.T) {
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
			if got := Text(tt.text); got != tt.want {
				t.Errorf("Text(%q)\n got %q\nwant %q", tt.text, got, tt.want)
			}
		})
	}
}
