package yamlenv

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentinel.env")
	body := "# secrets\n\nPLAIN=abc\nexport EXPORTED=def\nSINGLE='a $b \"c\"'\nDOUBLE=\"q\\\"x\\$y\\\\z\"\nKEPT=fromfile\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KEPT", "fromenv")
	for _, name := range []string{"PLAIN", "EXPORTED", "SINGLE", "DOUBLE"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	loaded, err := LoadEnvFile(path, []string{"PLAIN", "EXPORTED", "SINGLE", "DOUBLE", "KEPT"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"PLAIN", "EXPORTED", "SINGLE", "DOUBLE"}; !slices.Equal(loaded, want) {
		t.Errorf("loaded = %v, want %v", loaded, want)
	}
	for name, want := range map[string]string{"PLAIN": "abc", "EXPORTED": "def", "SINGLE": `a $b "c"`, "DOUBLE": `q"x$y\z`, "KEPT": "fromenv"} {
		if got := os.Getenv(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestLoadEnvFileMissingAndBad(t *testing.T) {
	if loaded, err := LoadEnvFile(filepath.Join(t.TempDir(), "none.env"), nil); err != nil || loaded != nil {
		t.Errorf("missing file: %v %v", loaded, err)
	}
	for name, body := range map[string]string{"no equals": "JUSTNAME\n", "bad name": "1BAD=x\n", "open quote": "A=\"x\n"} {
		path := filepath.Join(t.TempDir(), "bad.env")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadEnvFile(path, []string{"A", "JUSTNAME"}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestLoadEnvFileIgnoresNamesTheConfigDoesNotUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentinel.env")
	if err := os.WriteFile(path, []byte("WANTED_PW=ok\nLD_PRELOAD=/evil.so\nPATH=/evil\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("WANTED_PW")
	t.Cleanup(func() { os.Unsetenv("WANTED_PW") })
	origPath := os.Getenv("PATH")
	loaded, err := LoadEnvFile(path, []string{"WANTED_PW"})
	if err != nil || !slices.Equal(loaded, []string{"WANTED_PW"}) {
		t.Fatalf("loaded %v, err %v", loaded, err)
	}
	if os.Getenv("PATH") != origPath || os.Getenv("LD_PRELOAD") == "/evil.so" {
		t.Error("a name the config does not refer to was loaded")
	}
}

func TestReadEnvFileSetsNothingAndKeepsTheRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.env")
	content := "READ_ONLY_NEW=from-file\nREAD_ONLY_SET=from-file\nLD_PRELOAD=/evil.so\nNOT_WANTED=x\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("READ_ONLY_NEW")
	t.Setenv("READ_ONLY_SET", "from-environment")
	values, err := ReadEnvFile(path, []string{"READ_ONLY_NEW", "READ_ONLY_SET", "LD_PRELOAD"})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values["READ_ONLY_NEW"] != "from-file" {
		t.Errorf("values = %v, want only READ_ONLY_NEW (the environment wins, LD_PRELOAD is never read)", values)
	}
	if _, set := os.LookupEnv("READ_ONLY_NEW"); set {
		os.Unsetenv("READ_ONLY_NEW")
		t.Error("ReadEnvFile put a variable in the environment")
	}
}

func TestRefs(t *testing.T) {
	names, err := Refs([]byte("a: ${ONE}\nb: [\"x-${TWO}\", \"${ONE}\"]\n\"${KEY}\": plain\n"))
	if err != nil || !slices.Equal(names, []string{"ONE", "TWO"}) {
		t.Errorf("names = %v, err %v (a key is not expanded)", names, err)
	}
}
