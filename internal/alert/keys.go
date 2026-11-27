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

// PruneKey is the alert for a repository whose prune failed.
func PruneKey(repo string) string { return "prune:" + repo }

// StaleKey is the alert for a job with no recent successful backup.
func StaleKey(job string) string { return "stale:" + job }

// UptimeKey is the alert for an uptime check that is down.
func UptimeKey(check string) string { return "uptime:" + check }

// CertKey is the alert for a TLS certificate that expires soon.
func CertKey(check string) string { return "cert:" + check }

// FleetKey is the alert for a server of the fleet that cannot be reached. Server names hold no ":".
func FleetKey(server string) string { return "fleet:" + server }

// FleetFileKey is the alert for a fleet file the daemon cannot read, so it watches nothing. It has no
// ":" after "fleet", so it is never taken for the alert of a server called "config".
const FleetFileKey = "fleet-file"

// DiskKey is the alert for a file system that is nearly full; path is the first path watched on it.
func DiskKey(path string) string { return "disk:" + path }
