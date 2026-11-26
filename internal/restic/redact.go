package restic

import "regexp"

var (
	// userinfoRe matches the user and password of a URL: https://user:pass@host.
	userinfoRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@"']+@`)

	// secretValueRe matches name=value where the name says the value is a secret: the keys of an rclone
	// connection string (:s3,access_key_id=…,secret_access_key=…:bucket), a token in a query string, or
	// an environment variable. The value ends at a space, comma, colon or ampersand, or is quoted.
	secretValueRe = regexp.MustCompile(`(?i)\b([a-z0-9_]*(?:secret|passw(?:or)?d|pass|token|key|signature|credential)[a-z0-9_]*)=('[^']*'|"[^"]*"|[^\s,:&'"]+)`)
)

// Redact masks credentials in text that restic or a backend printed, before it is stored, logged or
// sent in an alert. Backend errors quote the repository address and the request that failed, and an
// rclone connection string or a URL can carry the keys inside it.
func Redact(text string) string {
	text = userinfoRe.ReplaceAllString(text, "${1}***@")
	return secretValueRe.ReplaceAllString(text, "${1}=***")
}
