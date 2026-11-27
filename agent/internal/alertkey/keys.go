// Package alertkey builds the keys of the alerts the agent raises: one form per kind of problem.
package alertkey

// Alert keys name an alert in the store (alert.Alert.Key), where open alerts survive a
// restart. Changing the form of a key orphans the alerts stored under the old one, so a
// rename needs a store migration that rewrites them.

// Backup is the alert for a failed backup of a job into a repository.
func Backup(job, repo string) string { return "backup:" + job + ":" + repo }

// Retention is the alert for a retention step that failed after a good backup.
func Retention(job, repo string) string { return "retention:" + job + ":" + repo }

// Verify is the alert for a repository that failed its integrity check.
func Verify(repo string) string { return "verify:" + repo }

// Prune is the alert for a repository whose prune failed.
func Prune(repo string) string { return "prune:" + repo }

// Stale is the alert for a job with no recent successful backup.
func Stale(job string) string { return "stale:" + job }

// Uptime is the alert for an uptime check that is down.
func Uptime(check string) string { return "uptime:" + check }

// Cert is the alert for a TLS certificate that expires soon.
func Cert(check string) string { return "cert:" + check }

// Fleet is the alert for a server of the fleet that cannot be reached. Server names hold no ":".
func Fleet(server string) string { return "fleet:" + server }

// FleetFile is the alert for a fleet file the daemon cannot read, so it watches nothing. It has no
// ":" after "fleet", so it is never taken for the alert of a server called "config".
const FleetFile = "fleet-file"

// Disk is the alert for a file system that is nearly full; path is the first path watched on it.
func Disk(path string) string { return "disk:" + path }
