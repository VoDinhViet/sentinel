package config

import (
	"os"
	"slices"
	"strings"
	"testing"
)

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

// Before the variables are set, a ${VAR} in a number does not decode; that is not what MissingVars is for.
func TestMissingVarsIgnoresValuesThatDoNotDecodeYet(t *testing.T) {
	os.Unsetenv("PG_PORT_NOT_SET")
	missing, err := MissingVars([]byte("repositories:\n  r: {url: /r, password: p}\njobs:\n  - {name: db, type: postgres, postgres: {database: d, port: \"${PG_PORT_NOT_SET}\"}}\n"))
	if err != nil || len(missing) != 1 || missing[0] != "PG_PORT_NOT_SET" {
		t.Errorf("missing = %v, err = %v", missing, err)
	}
}
