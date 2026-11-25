package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The daemon runs as root, so a config that another user can write is a way to run commands as root
// (repositories.*.env, hook-like values), and one every user can read may hold passwords.
func TestConfigAccessFlagsFilesOthersCanWriteOrRead(t *testing.T) {
	tests := []struct {
		name     string
		mode     os.FileMode
		wantErr  bool
		wantWarn bool
	}{
		{"owner only", 0o600, false, false},
		{"group may read", 0o640, false, false},
		{"everyone can read", 0o644, true, true},
		{"group can write, as a private group gives by default", 0o664, true, true},
		{"everyone can write", 0o666, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("jobs: []\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}
			finding := ConfigAccess(path)
			if finding.Name != "config" || finding.Info != path {
				t.Errorf("finding = %+v, want the config finding with its path", finding)
			}
			if (finding.Err != nil) != tt.wantErr || finding.Warn != tt.wantWarn {
				t.Errorf("mode %o: err=%v warn=%v, want err=%v warn=%v", tt.mode, finding.Err, finding.Warn, tt.wantErr, tt.wantWarn)
			}
			if tt.wantErr && !strings.Contains(finding.Hint, "chmod 640 "+path) {
				t.Errorf("hint = %q, want the chmod command", finding.Hint)
			}
		})
	}
}

func TestConfigAccessIgnoresAFileItCannotStat(t *testing.T) {
	finding := ConfigAccess(filepath.Join(t.TempDir(), "missing.yaml"))
	if finding.Err != nil || finding.Name != "config" {
		t.Errorf("finding = %+v, a missing file is reported by the loader, not here", finding)
	}
}
