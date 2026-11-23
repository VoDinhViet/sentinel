package alert

import "testing"

// The keys are stored in the database: these exact forms must not change without a migration.
func TestKeysKeepTheirStoredForm(t *testing.T) {
	for name, tc := range map[string]struct{ got, want string }{
		"backup":    {BackupKey("appdb", "s3"), "backup:appdb:s3"},
		"retention": {RetentionKey("appdb", "s3"), "retention:appdb:s3"},
		"verify":    {VerifyKey("s3"), "verify:s3"},
		"stale":     {StaleKey("appdb"), "stale:appdb"},
		"uptime":    {UptimeKey("site"), "uptime:site"},
		"cert":      {CertKey("site"), "cert:site"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s key = %q, want %q", name, tc.got, tc.want)
		}
	}
}
