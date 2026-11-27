package restic

import "sentinel/pkg/redact"

// Redact masks credentials in text that restic or a backend printed, before it is stored, logged or
// sent in an alert. See redact.Text.
func Redact(text string) string { return redact.Text(text) }
