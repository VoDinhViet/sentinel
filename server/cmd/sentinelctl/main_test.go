package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/notify"
	"sentinel/pkg/pki"
	"sentinel/server/internal/app"
	"sentinel/server/internal/config"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/httpapi"
	"sentinel/server/internal/terminal"
)

const loginPassword = "correct horse battery"

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// plant is a control plane with its sentinelctl listener, and a directory for sentinelctl.
type plant struct {
	t           *testing.T
	url         string
	fingerprint string
	dir         string
	app         *app.App
}

func newPlant(t *testing.T) *plant {
	t.Helper()
	cfg := config.Default()
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.Hosts = []string{"127.0.0.1"}
	if _, err := app.Init(context.Background(), &cfg, loginPassword); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(context.Background(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	cert, err := a.ServerCert(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(httpapi.New(a).CtlHandler())
	server.TLS = pki.CtlTLSConfig(cert)
	server.StartTLS()
	t.Cleanup(server.Close)
	return &plant{t: t, url: server.URL, fingerprint: a.CA.Fingerprint(), dir: t.TempDir(), app: a}
}

// run executes a sentinelctl command line with stdin as the typed or piped input.
func (p *plant) run(stdin string, args ...string) (string, error) {
	p.t.Helper()
	c := &cli{term: terminal.Terminal{In: strings.NewReader(stdin), Out: io.Discard}}
	root := newRootCmd(c)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--dir", p.dir}, args...))
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func (p *plant) login() {
	p.t.Helper()
	if out, err := p.run(loginPassword+"\n", "login", "--url", p.url, "--ca-fingerprint", p.fingerprint, "--password-stdin"); err != nil {
		p.t.Fatalf("login: %v\n%s", err, out)
	}
}

func TestLoginSavesTheConfigAndASessionOthersCannotRead(t *testing.T) {
	p := newPlant(t)
	p.login()
	info, err := os.Stat(filepath.Join(p.dir, "session"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session file: %v, %v", info, err)
	}
	config, _ := os.ReadFile(filepath.Join(p.dir, "ctl.yaml"))
	if !strings.Contains(string(config), p.fingerprint) || strings.Contains(string(config), loginPassword) {
		t.Errorf("ctl.yaml = %q", config)
	}
	// Later commands need no flags.
	if out, err := p.run("", "server", "ls"); err != nil || !strings.Contains(out, "Chưa có server nào") {
		t.Errorf("server ls: %v\n%s", err, out)
	}
}

func TestLoginRefusesAWrongPasswordAndACAThatIsNotTheOneGiven(t *testing.T) {
	p := newPlant(t)
	if _, err := p.run("not the password\n", "login", "--url", p.url, "--ca-fingerprint", p.fingerprint, "--password-stdin"); err == nil {
		t.Error("a wrong password logged in")
	}
	other, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.run(loginPassword+"\n", "login", "--url", p.url, "--ca-fingerprint", other.Fingerprint(), "--password-stdin"); err == nil {
		t.Error("a server whose CA is not the one pinned was trusted")
	}
	if _, err := os.Stat(filepath.Join(p.dir, "session")); err == nil {
		t.Error("a failed login left a session")
	}
	if _, err := p.run("", "login"); err == nil {
		t.Error("a first login without --url and --ca-fingerprint ran")
	}
}

func TestAddListAndRevokeAServer(t *testing.T) {
	p := newPlant(t)
	p.login()
	out, err := p.run("", "server", "add", "web01")
	if err != nil || !strings.Contains(out, "sentinel agent join --url") || !strings.Contains(out, "--ca-fingerprint "+p.fingerprint) {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, _ := p.run("", "server", "ls"); !strings.Contains(out, "web01") || !strings.Contains(out, "chờ join") {
		t.Errorf("ls after add:\n%s", out)
	}
	if _, err := p.run("web02\n", "server", "revoke", "web01"); err == nil {
		t.Error("revoke ran after the wrong name was typed")
	}
	if out, _ := p.run("", "server", "ls"); strings.Contains(out, "đã thu hồi") {
		t.Errorf("a refused revoke changed something:\n%s", out)
	}
	if _, err := p.run("web01\n", "server", "revoke", "web01"); err != nil {
		t.Fatalf("revoke with the name typed: %v", err)
	}
	if _, err := p.run("", "server", "revoke", "web01", "--yes"); err != nil {
		t.Errorf("revoking twice: %v", err)
	}
	if _, err := p.run("", "server", "revoke", "nobody", "--yes"); err == nil {
		t.Error("revoking a server that does not exist worked")
	}
	var agents []ctlapi.Agent
	out, _ = p.run("", "server", "ls", "-o", "json")
	if err := json.Unmarshal([]byte(out), &agents); err != nil || len(agents) != 1 || agents[0].State != "revoked" {
		t.Errorf("ls -o json: %v\n%s", err, out)
	}
	if _, err := p.run("", "server", "add", "bad/name"); err == nil {
		t.Error("a bad name was accepted")
	}
	if _, err := p.run("", "server", "ls", "-o", "yaml"); err == nil {
		t.Error("an unknown output format was accepted")
	}
}

func TestAuditListsWhatWasDoneAndVerifies(t *testing.T) {
	p := newPlant(t)
	p.login()
	if _, err := p.run("", "server", "add", "web01"); err != nil {
		t.Fatal(err)
	}
	out, err := p.run("", "audit", "-n", "10")
	if err != nil || !strings.Contains(out, "login.ok") || !strings.Contains(out, "agent.enroll") || !strings.Contains(out, "web01") {
		t.Errorf("audit: %v\n%s", err, out)
	}
	if out, err := p.run("", "audit", "verify"); err != nil || !strings.Contains(out, "nguyên vẹn") {
		t.Errorf("audit verify: %v\n%s", err, out)
	}
	if _, err := p.run("", "audit", "-n", "0"); err == nil {
		t.Error("a limit of 0 was accepted")
	}
}

func TestPasswdChangesThePasswordAndEndsTheSession(t *testing.T) {
	p := newPlant(t)
	p.login()
	if _, err := p.run("wrong wrong wrong\nanother long password\nanother long password\n", "passwd", "--password-stdin"); err == nil {
		t.Error("a wrong current password changed it")
	}
	if _, err := p.run(loginPassword+"\nanother long password\n", "passwd", "--password-stdin"); err != nil {
		t.Fatalf("passwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "session")); err == nil {
		t.Error("the session file stayed after the password changed")
	}
	if _, err := p.run("", "server", "ls"); err == nil || !strings.Contains(err.Error(), "login") {
		t.Errorf("server ls after passwd: %v, want a hint to log in", err)
	}
	if _, err := p.run("another long password\n", "login", "--password-stdin"); err != nil {
		t.Errorf("login with the new password (the saved url and fingerprint): %v", err)
	}
}

func TestLogoutEndsTheSessionAndAnEndedSessionIsExplained(t *testing.T) {
	p := newPlant(t)
	p.login()
	token, _ := os.ReadFile(filepath.Join(p.dir, "session"))
	if _, err := p.run("", "logout"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "session")); err == nil {
		t.Error("logout left the session file")
	}
	if out, err := p.run("", "logout"); err != nil || !strings.Contains(out, "Chưa đăng nhập") {
		t.Errorf("logout twice: %v\n%s", err, out)
	}
	// The old token is not a session any more.
	if err := os.WriteFile(filepath.Join(p.dir, "session"), token, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.run("", "server", "ls"); err == nil || !strings.Contains(err.Error(), "hết hạn") {
		t.Errorf("server ls with an ended session: %v", err)
	}
}

func TestASessionFileOthersCanReadIsRefused(t *testing.T) {
	p := newPlant(t)
	p.login()
	if err := os.Chmod(filepath.Join(p.dir, "session"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.run("", "server", "ls"); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("err = %v, want the file to be refused with a hint", err)
	}
}

func TestShowAndAlertTellWhatTheServerReportedAndWhatIsOpen(t *testing.T) {
	p := newPlant(t)
	p.login()
	if _, err := p.run("", "server", "add", "web01"); err != nil {
		t.Fatal(err)
	}
	if out, err := p.run("", "server", "show", "web01"); err != nil || !strings.Contains(out, "PENDING") || !strings.Contains(out, "Chưa nhận báo cáo") {
		t.Fatalf("show before any report: %v\n%s", err, out)
	}
	report := agentapi.Report{
		Version: "1.2.3",
		Host:    agentapi.HostStatus{Hostname: "web01.example.com", CPUs: 4, Memory: agentapi.MemoryStatus{TotalBytes: 8 << 30, AvailableBytes: 3 << 30}, Disks: []agentapi.DiskStatus{{Mount: "/", FreeBytes: 20 << 30, TotalBytes: 100 << 30}}},
		Backups: []agentapi.BackupStatus{{Job: "db", Repository: "s3", FinishedAt: time.Now(), Success: false, Result: "FAILED: disk full"}},
		Checks:  []agentapi.CheckStatus{{Name: "site", OK: false, Error: "timeout", At: time.Now()}},
		Alerts:  []agentapi.AlertStatus{{Key: "backup:db:s3", Message: "backup db failed", Since: time.Now()}},
	}
	if err := p.app.Inventory.SaveReport(context.Background(), "web01", report, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err := p.run("", "server", "show", "web01")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"web01.example.com", "1.2.3", "3.0 GiB trống / 8.0 GiB", "FAILED: disk full", "DOWN: timeout", "backup:db:s3"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if _, err := p.run("", "server", "show", "nobody"); err == nil {
		t.Error("showing a server that does not exist worked")
	}

	if out, _ := p.run("", "alert"); !strings.Contains(out, "Không có cảnh báo") {
		t.Errorf("alert with none open:\n%s", out)
	}
	p.app.Alerts.Fire(context.Background(), "agent:web01", notify.Critical, "web01 is offline", "")
	if out, _ := p.run("", "alert"); !strings.Contains(out, "agent:web01") || !strings.Contains(out, "web01 is offline") {
		t.Errorf("alert with one open:\n%s", out)
	}
	var detail ctlapi.AgentDetail
	out, _ = p.run("", "server", "show", "web01", "-o", "json")
	if err := json.Unmarshal([]byte(out), &detail); err != nil || detail.Report == nil || detail.Report.Version != "1.2.3" {
		t.Errorf("show -o json: %v\n%s", err, out)
	}
}
