package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// `backup restore --target /` overwrites the live system as root, so it needs --force. The check runs
// before the config is read, so a sentinel error from loadConfig tells that the command got past it.
func TestRestoreAsksForForceOnASystemFolder(t *testing.T) {
	errNoConfig := errors.New("no config")
	tests := []struct {
		name       string
		args       []string
		wantSystem bool
	}{
		{"root of the system", []string{"--target", "/"}, true},
		{"etc", []string{"--target", "/etc/"}, true},
		{"var with force", []string{"--target", "/var", "--force"}, false},
		{"root with force", []string{"--target", "/", "--force"}, false},
		{"scratch folder", []string{"--target", "/tmp/restore"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := (&cliApp{configErr: errNoConfig}).backupCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(append([]string{"restore", "db", "latest"}, tt.args...))
			err := cmd.Execute()
			if tt.wantSystem {
				if err == nil || !strings.Contains(err.Error(), "system folder") || !strings.Contains(err.Error(), "--force") {
					t.Errorf("err = %v, want a refusal that names --force", err)
				}
				return
			}
			if !errors.Is(err, errNoConfig) {
				t.Errorf("err = %v, want it to get past the target check (to the config error)", err)
			}
		})
	}
}
