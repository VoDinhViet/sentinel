package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sentinel/pkg/pki"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/config"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.Hosts = []string{"127.0.0.1"}
	return &cfg
}

func TestInitMakesACompleteControlPlaneThatOpens(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	result, err := Init(ctx, cfg, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	files := cfg.Files()
	for _, path := range []string{files.CAKey(), files.ServerKey(), files.DB()} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, %v; want 0600", path, info.Mode().Perm(), err)
		}
	}
	if info, err := os.Stat(cfg.StateDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("state dir: mode %v, %v; want 0700", info.Mode().Perm(), err)
	}

	app, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.CA.Fingerprint() != result.Fingerprint {
		t.Errorf("fingerprint = %s, want %s", app.CA.Fingerprint(), result.Fingerprint)
	}
	if _, _, err := app.Auth.Login(ctx, "10.0.0.1", "correct horse battery"); err != nil {
		t.Errorf("the password set at init does not work: %v", err)
	}
	if checked, err := audit.Verify(ctx, app.Store.DB); err != nil || checked < 2 {
		t.Errorf("audit: %d rows, %v; want at least server.init and password.set", checked, err)
	}
	// The server certificate is for the hosts and chains to the CA.
	chain, _ := os.ReadFile(files.ServerCert())
	key, _ := os.ReadFile(files.ServerKey())
	if _, err := tlsPair(chain, key); err != nil {
		t.Errorf("the server certificate and key do not make a pair: %v", err)
	}
}

func TestInitRefusesToOverwriteAndOpenNeedsInit(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	if _, err := Open(ctx, cfg); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("Open before init: err = %v", err)
	}
	if _, err := Init(ctx, cfg, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cfg.Files().CAKey())
	if _, err := Init(ctx, cfg, "another long password"); !errors.Is(err, ErrAlreadyInitialized) {
		t.Errorf("second init: err = %v, want it refused", err)
	}
	if after, _ := os.ReadFile(cfg.Files().CAKey()); string(after) != string(before) {
		t.Error("a second init replaced the CA key")
	}
}

func TestInitWithAShortPasswordOrBadHostLeavesNothing(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	if _, err := Init(ctx, cfg, "short"); err == nil {
		t.Error("a short password was accepted")
	}
	if _, err := os.Stat(cfg.StateDir); err == nil {
		t.Error("the state directory was made for a password that was refused")
	}
	cfg.Hosts = nil
	if _, err := Init(ctx, cfg, "correct horse battery"); err == nil {
		t.Error("a server certificate with no host was made")
	}
	entries, _ := os.ReadDir(cfg.StateDir)
	if len(entries) != 0 {
		t.Errorf("a failed init left %d files behind", len(entries))
	}
	// and it can be run again after fixing the problem
	cfg.Hosts = []string{"127.0.0.1"}
	if _, err := Init(ctx, cfg, "correct horse battery"); err != nil {
		t.Errorf("init after a failed one: %v", err)
	}
}

func TestEnrollThroughTheAppUsesTheCAFromInit(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	result, err := Init(ctx, cfg, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	token, _, err := app.Enroll.Create(ctx, "web-01")
	if err != nil {
		t.Fatal(err)
	}
	_, csr, _ := pki.NewKeyAndCSR()
	joined, err := app.Enroll.Join(ctx, token, csr)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := pki.ParseCert(joined.CertPEM)
	if _, err := cert.Verify(verifyOptions(app.CA)); err != nil {
		t.Errorf("the agent certificate does not chain to the CA from init: %v", err)
	}
	if app.CA.Fingerprint() != result.Fingerprint {
		t.Error("the CA changed")
	}
}

func TestServerCertIsKeptWhileItFitsAndReissuedWhenItDoesNot(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	if _, err := Init(ctx, cfg, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	a, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	first, err := a.ServerCert(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.ServerCert(ctx, time.Now())
	if err != nil || again.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) != 0 {
		t.Errorf("a certificate that fits was replaced (err %v)", err)
	}
	for _, loopback := range []string{"localhost", "127.0.0.1", "::1"} {
		if err := first.Leaf.VerifyHostname(loopback); err != nil {
			t.Errorf("the certificate does not cover %s: %v", loopback, err)
		}
	}

	nearEnd := first.Leaf.NotAfter.Add(-pki.RenewBefore + time.Hour)
	renewed, err := a.ServerCert(ctx, nearEnd)
	if err != nil || renewed.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) == 0 {
		t.Fatalf("a certificate near its end was kept (err %v)", err)
	}
	if err := renewed.Leaf.CheckSignatureFrom(a.CA.Cert); err != nil {
		t.Errorf("the new certificate is not signed by the same CA: %v", err)
	}

	a.Config.Hosts = append(a.Config.Hosts, "new.example.com")
	withNewHost, err := a.ServerCert(ctx, time.Now())
	if err != nil || withNewHost.Leaf.VerifyHostname("new.example.com") != nil {
		t.Errorf("a host added to the config is not in the certificate (err %v)", err)
	}
}

func TestTheCertificateOfARunningServerIsRenewedWithoutARestart(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	if _, err := Init(ctx, cfg, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	a, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	source, err := a.NewServerCertSource(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := source.Get(nil)
	if err := source.Refresh(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if same, _ := source.Get(nil); same.Leaf.SerialNumber.Cmp(before.Leaf.SerialNumber) != 0 {
		t.Error("a certificate that is not near its end was replaced")
	}
	if err := source.Refresh(ctx, before.Leaf.NotAfter.Add(-pki.RenewBefore+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if after, _ := source.Get(nil); after.Leaf.SerialNumber.Cmp(before.Leaf.SerialNumber) == 0 {
		t.Error("the certificate near its end was not replaced for new connections")
	}
}
