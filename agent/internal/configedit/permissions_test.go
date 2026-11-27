package configedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/agent/internal/config"
)

// An env file that other users could read is tightened when a secret is written to it: the
// prompt promises "readable only by you", so 0644 must not survive. Group read stays because the
// daemon may run as a user in that group.
func TestSecretWritesTightenAWorldReadableEnvFile(t *testing.T) {
	tests := []struct {
		name  string
		write func(envFile string) error
	}{
		{"SaveSecret", func(envFile string) error { return SaveSecret(envFile, "TG_TOKEN", "abc") }},
		{"EnsureSecret", func(envFile string) error { _, err := EnsureSecret(envFile, "TG_TOKEN"); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for existing, want := range map[os.FileMode]os.FileMode{0o644: 0o640, 0o666: 0o640, 0o640: 0o640, 0o600: 0o600} {
				envFile := filepath.Join(t.TempDir(), "sentinel.env")
				if err := os.WriteFile(envFile, []byte("A=1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(envFile, existing); err != nil {
					t.Fatal(err)
				}
				if err := tt.write(envFile); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(envFile)
				if err != nil || info.Mode().Perm() != want {
					t.Errorf("env file was %o, now %v (%v), want %o", existing, info.Mode().Perm(), err, want)
				}
			}
		})
	}
}

// The .bak holds the whole previous config, literal passwords included, so a .bak left by an
// earlier run with a wider mode must not stay readable. A config that is a symlink keeps its link.
func TestSaveTightensAnOldBackupAndKeepsASymlinkedConfig(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	link := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(real, []byte(baseConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(real, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real+".bak", []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(real+".bak", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Save(link, "", Job{Name: "a", Type: config.JobPostgres, Database: "a", Repositories: []string{"s3"}}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced: %v %v", info, err)
	}
	if text := readFile(t, real); !strings.Contains(text, "name: a") {
		t.Errorf("the file behind the link was not updated:\n%s", text)
	}
	if got := readFile(t, real+".bak"); got != baseConfig {
		t.Errorf(".bak = %q, want the previous config", got)
	}
	if info, err := os.Stat(real + ".bak"); err != nil || info.Mode().Perm() != 0o640 {
		t.Errorf(".bak mode = %v (%v), want 0640 like the config", info.Mode().Perm(), err)
	}
}
