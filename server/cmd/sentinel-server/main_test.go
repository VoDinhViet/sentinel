package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/server/internal/app"
	"sentinel/server/internal/auth"
	"sentinel/server/internal/config"
	"sentinel/server/internal/terminal"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// run executes a command line against a config in dir, with stdin as the typed or piped input.
func run(t *testing.T, dir, stdin string, args ...string) (string, error) {
	t.Helper()
	c := &cli{term: terminal.Terminal{In: strings.NewReader(stdin), Out: io.Discard}}
	root := newRootCmd(c)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--config", filepath.Join(dir, "server.yaml")}, args...))
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func initServer(t *testing.T) (dir, out string) {
	t.Helper()
	dir = t.TempDir()
	out, err := run(t, dir, "correct horse battery\n", "init", "--host", "backup.example.com", "--state-dir", filepath.Join(dir, "state"), "--password-stdin")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return dir, out
}

func TestInitEnrollPasswdAndVerifyFromTheCommandLine(t *testing.T) {
	dir, out := initServer(t)
	cfg, err := config.Load(filepath.Join(dir, "server.yaml"))
	if err != nil {
		t.Fatalf("init wrote no usable config: %v", err)
	}
	a, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := a.CA.Fingerprint()
	a.Close()
	if !strings.Contains(out, fingerprint) {
		t.Errorf("init did not print the fingerprint of the CA:\n%s", out)
	}

	out, err = run(t, dir, "", "enroll", "web-01")
	if err != nil {
		t.Fatalf("enroll: %v\n%s", err, out)
	}
	for _, want := range []string{"sentinel agent join", "--url https://backup.example.com:9320", "--token ", "--ca-fingerprint " + fingerprint} {
		if !strings.Contains(out, want) {
			t.Errorf("the join command lacks %q:\n%s", want, out)
		}
	}

	if out, err := run(t, dir, "a brand new password\n", "passwd", "--password-stdin"); err != nil {
		t.Fatalf("passwd: %v\n%s", err, out)
	}
	a, _ = app.Open(context.Background(), cfg)
	defer a.Close()
	if _, _, err := a.Auth.Login(context.Background(), "10.0.0.1", "correct horse battery"); !errors.Is(err, auth.ErrBadPassword) {
		t.Errorf("the old password still works: %v", err)
	}
	if _, _, err := a.Auth.Login(context.Background(), "10.0.0.1", "a brand new password"); err != nil {
		t.Errorf("the new password: %v", err)
	}

	out, err = run(t, dir, "", "audit", "verify")
	if err != nil || !strings.Contains(out, "nguyên vẹn") {
		t.Errorf("audit verify: %v\n%s", err, out)
	}
}

func TestAuditVerifyFailsOnATamperedLog(t *testing.T) {
	dir, _ := initServer(t)
	cfg, _ := config.Load(filepath.Join(dir, "server.yaml"))
	a, _ := app.Open(context.Background(), cfg)
	for _, drop := range []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET detail = 'edited' WHERE id = 1`} {
		if _, err := a.Store.DB.Exec(drop); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	if out, err := run(t, dir, "", "audit", "verify"); err == nil || !strings.Contains(err.Error(), "hỏng") {
		t.Errorf("a tampered log verified: %v\n%s", err, out)
	}
}

func TestInitRefusesTwiceAndEnrollRefusesAJoinedNameAndNeedsInit(t *testing.T) {
	dir, _ := initServer(t)
	if _, err := run(t, dir, "correct horse battery\n", "init", "--host", "x.example.com", "--state-dir", filepath.Join(dir, "state"), "--password-stdin"); err == nil {
		t.Error("init ran over an initialized server")
	}
	if _, err := run(t, dir, "", "enroll", "bad/name"); err == nil {
		t.Error("an invalid name was enrolled")
	}
	if _, err := run(t, t.TempDir(), "", "enroll", "web-01"); err == nil || !strings.Contains(err.Error(), "init") {
		t.Errorf("enroll without init: err = %v, want a hint to run init", err)
	}
}

func TestInitNeedsAHostAndAGoodPassword(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if _, err := run(t, dir, "correct horse battery\n", "init", "--state-dir", state, "--password-stdin"); err == nil {
		t.Error("init without --host ran")
	}
	if _, err := run(t, dir, "short\n", "init", "--host", "a.example.com", "--state-dir", state, "--password-stdin"); err == nil {
		t.Error("a short password was accepted")
	}
	if _, err := os.Stat(state); err == nil {
		t.Error("a refused init made the state directory")
	}
}
