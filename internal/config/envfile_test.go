package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestMissingVarsAndPasswordVars(t *testing.T) {
	t.Setenv("SET_ONE", "x")
	raw := []byte("repositories:\n  local:\n    url: /r\n    password: \"${PASS_A}\"\n  s3:\n    url: s3:x\n    password: \"pre-${PASS_B}\"\n  fixed:\n    url: /f\n    password: plain\nstore: ${SET_ONE}/db\nserver_name: ${ZED}\n")
	os.Unsetenv("PASS_A")
	os.Unsetenv("PASS_B")
	os.Unsetenv("ZED")
	missing, err := MissingVars(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"PASS_A", "PASS_B", "ZED"}; !slices.Equal(missing, want) {
		t.Errorf("missing = %v, want %v", missing, want)
	}
	vars, err := PasswordVars(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 1 || vars["local"] != "PASS_A" {
		t.Errorf("password vars = %v, want only local → PASS_A", vars)
	}
}

func TestMissingVarsRejectsAnUnknownKeyFirst(t *testing.T) {
	os.Unsetenv("OLD_FORMAT_PW")
	_, err := MissingVars([]byte("repositories:\n  local: {url: /r, password: \"${OLD_FORMAT_PW}\"}\nbackups:\n  - name: documents\n"))
	if err == nil || !strings.Contains(err.Error(), "field backups not found") {
		t.Errorf("err = %v, want the unknown key", err)
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

func TestRefVars(t *testing.T) {
	names, err := RefVars([]byte("a: ${ONE}\nb: [\"x-${TWO}\", \"${ONE}\"]\n\"${KEY}\": plain\n"))
	if err != nil || !slices.Equal(names, []string{"ONE", "TWO"}) {
		t.Errorf("names = %v, err %v (a key is not expanded)", names, err)
	}
}

// Before the variables are set, a ${VAR} in a number does not decode; that is not what MissingVars is for.
func TestMissingVarsIgnoresValuesThatDoNotDecodeYet(t *testing.T) {
	os.Unsetenv("PG_PORT_NOT_SET")
	missing, err := MissingVars([]byte("repositories:\n  r: {url: /r, password: p}\njobs:\n  - {name: db, type: postgres, postgres: {database: d, port: \"${PG_PORT_NOT_SET}\"}}\n"))
	if err != nil || len(missing) != 1 || missing[0] != "PG_PORT_NOT_SET" {
		t.Errorf("missing = %v, err = %v", missing, err)
	}
}
