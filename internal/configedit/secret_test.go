package configedit

import (
	"os"
	"path/filepath"
	"testing"

	"sentinel/internal/config"
)

func TestSaveSecretAddsReplacesAndKeepsTheRest(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "conf", "sentinel.env")
	if err := SaveSecret(envFile, "RESTIC_PASSWORD", "first"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("# note\nOTHER=1\nexport RESTIC_PASSWORD=old\nTAIL=2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveSecret(envFile, "RESTIC_PASSWORD", "new"); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, envFile), "# note\nOTHER=1\nRESTIC_PASSWORD=new\nTAIL=2\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	if err := SaveSecret(envFile, "PG_PASSWORD", "x"); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, envFile), "# note\nOTHER=1\nRESTIC_PASSWORD=new\nTAIL=2\nPG_PASSWORD=x\n"; got != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	if info, err := os.Stat(envFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if dirInfo, err := os.Stat(filepath.Dir(envFile)); err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, %v; want 0700", dirInfo.Mode().Perm(), err)
	}
}

func TestSaveSecretValuesRoundTripThroughLoadEnvFile(t *testing.T) {
	for _, value := range []string{"pass123456", `has space`, `quo"te`, `dollar$HOME`, `it's`, `back\slash`, "tick`s", `a#b`} {
		envFile := filepath.Join(t.TempDir(), "sentinel.env")
		if err := SaveSecret(envFile, "SECRET_ROUNDTRIP", value); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		os.Unsetenv("SECRET_ROUNDTRIP")
		if _, err := config.LoadEnvFile(envFile); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if got := os.Getenv("SECRET_ROUNDTRIP"); got != value {
			t.Errorf("round trip of %q gave %q (file %q)", value, got, readFile(t, envFile))
		}
	}
	os.Unsetenv("SECRET_ROUNDTRIP")
}

func TestSaveSecretRejectsBadInput(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "sentinel.env")
	if err := SaveSecret(envFile, "bad name", "x"); err == nil {
		t.Error("a bad variable name must be refused")
	}
	if err := SaveSecret(envFile, "OK", "two\nlines"); err == nil {
		t.Error("a line break must be refused")
	}
}
