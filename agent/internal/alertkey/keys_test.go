package alertkey

import "testing"

// The keys are stored in the database: these exact forms must not change without a migration.
func TestKeysKeepTheirStoredForm(t *testing.T) {
	for name, tc := range map[string]struct{ got, want string }{
		"backup":    {Backup("appdb", "s3"), "backup:appdb:s3"},
		"retention": {Retention("appdb", "s3"), "retention:appdb:s3"},
		"verify":    {Verify("s3"), "verify:s3"},
		"stale":     {Stale("appdb"), "stale:appdb"},
		"uptime":    {Uptime("site"), "uptime:site"},
		"cert":      {Cert("site"), "cert:site"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s key = %q, want %q", name, tc.got, tc.want)
		}
	}
}
