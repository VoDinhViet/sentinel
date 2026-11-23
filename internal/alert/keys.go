package alert

// Alert keys name an alert in the store (store.Alert.Key), where open alerts survive a
// restart. Changing the form of a key orphans the alerts stored under the old one, so a
// rename needs a store migration that rewrites them.

// BackupKey is the alert for a failed backup of a job into a repository.
func BackupKey(job, repo string) string { return "backup:" + job + ":" + repo }

// RetentionKey is the alert for a retention step that failed after a good backup.
func RetentionKey(job, repo string) string { return "retention:" + job + ":" + repo }

// VerifyKey is the alert for a repository that failed its integrity check.
func VerifyKey(repo string) string { return "verify:" + repo }

// StaleKey is the alert for a job with no recent successful backup.
func StaleKey(job string) string { return "stale:" + job }

// UptimeKey is the alert for an uptime check that is down.
func UptimeKey(check string) string { return "uptime:" + check }

// CertKey is the alert for a TLS certificate that expires soon.
func CertKey(check string) string { return "cert:" + check }
