package yamlenv

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestIsDangerousName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"PATH", true},
		{"path", true},
		{"LD_PRELOAD", true},
		{"LD_LIBRARY_PATH", true},
		{"DYLD_INSERT_LIBRARIES", true},
		{"BASH_ENV", true},
		{"IFS", true},
		{"RESTIC_PASSWORD_COMMAND", true},
		{"RESTIC_FROM_PASSWORD_COMMAND", true},
		{"AWS_ACCESS_KEY_ID", false},
		{"AWS_SECRET_ACCESS_KEY", false},
		{"RCLONE_CONFIG_S3_TYPE", false},
		{"RESTIC_PASSWORD", false},
		{"PGPASSWORD", false},
		{"TG_TOKEN", false},
		{"MYPATH", false},
		{"SSLKEYLOGFILE", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDangerousName(tt.name); got != tt.want {
				t.Errorf("IsDangerousName(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestLoadEnvFileNeverSetsDangerousVariables(t *testing.T) {
	for _, name := range []string{"BASH_ENV", "RESTIC_PASSWORD_COMMAND", "LD_PRELOAD"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	t.Setenv("PW_FROM_FILE", "")
	os.Unsetenv("PW_FROM_FILE")
	path := filepath.Join(t.TempDir(), "sentinel.env")
	content := "BASH_ENV=/tmp/evil.sh\nRESTIC_PASSWORD_COMMAND=cat /etc/shadow\nLD_PRELOAD=/tmp/evil.so\nPW_FROM_FILE=ok\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	wanted := []string{"BASH_ENV", "RESTIC_PASSWORD_COMMAND", "LD_PRELOAD", "PW_FROM_FILE"}
	loaded, err := LoadEnvFile(path, wanted)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loaded, []string{"PW_FROM_FILE"}) {
		t.Errorf("loaded = %v, want only PW_FROM_FILE", loaded)
	}
	for _, name := range []string{"BASH_ENV", "RESTIC_PASSWORD_COMMAND", "LD_PRELOAD"} {
		if value, set := os.LookupEnv(name); set {
			t.Errorf("%s was set to %q from the env file", name, value)
		}
	}
	if os.Getenv("PW_FROM_FILE") != "ok" {
		t.Error("an ordinary variable must still be loaded")
	}
}
