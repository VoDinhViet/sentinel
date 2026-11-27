package doctor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/store"
)

func TestRunReportsProblemsWithHints(t *testing.T) {
	cfg := &config.Config{
		StorePath:    filepath.Join(t.TempDir(), "state.db"),
		Repositories: map[string]config.Repository{"local": {URL: filepath.Join(t.TempDir(), "repo"), Password: "x"}},
	}
	findings := Run(context.Background(), cfg, "/etc/sentinel/config.yaml", func(c *config.Config) (*store.Store, error) { return store.Open(c.StorePath) })

	byName := map[string]Finding{}
	for _, f := range findings {
		byName[f.Name] = f
	}
	if f := byName["config"]; f.Err != nil || f.Info != "/etc/sentinel/config.yaml" {
		t.Errorf("config = %+v", f)
	}
	if f := byName["store"]; f.Err != nil {
		t.Errorf("store = %+v", f)
	}
	notifiers := byName["notifiers"]
	if notifiers.Err == nil || !notifiers.Warn || !strings.Contains(notifiers.Hint, "notifiers:") {
		t.Errorf("notifiers = %+v (a missing notifier is a warning with a hint)", notifiers)
	}
	repo := byName["repository local"]
	if repo.Err == nil || repo.Hint == "" {
		t.Errorf("repository = %+v (a repository that cannot be used says what to check)", repo)
	}
}

func TestRunStoreFailureHasAHint(t *testing.T) {
	cfg := &config.Config{StorePath: "/proc/nope/state.db"}
	findings := Run(context.Background(), cfg, "c.yaml", func(c *config.Config) (*store.Store, error) { return store.Open(c.StorePath) })
	for _, f := range findings {
		if f.Name == "store" {
			if f.Err == nil || f.Hint == "" {
				t.Errorf("store = %+v, want an error and a hint", f)
			}
			return
		}
	}
	t.Error("no store finding")
}

func TestLocaleWarnsUnlessUTF8(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		warn bool
	}{
		{"unset", map[string]string{}, true},
		{"posix", map[string]string{"LANG": "C"}, true},
		{"utf8", map[string]string{"LANG": "en_US.UTF-8"}, false},
		{"lowercase spelling", map[string]string{"LANG": "C.utf8"}, false},
		{"LC_ALL wins over LANG", map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, true},
		{"LC_ALL utf8 wins", map[string]string{"LC_ALL": "C.UTF-8", "LANG": "C"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			finding := Locale(func(name string) string { return test.env[name] })
			if (finding.Err != nil) != test.warn || (test.warn && (!finding.Warn || finding.Hint == "")) {
				t.Errorf("Locale(%v) = %+v, want warn %v", test.env, finding, test.warn)
			}
		})
	}
}

func TestHeartbeatFindingWarnsWhenNothingWatchesTheDaemon(t *testing.T) {
	off := heartbeatFinding(&config.Config{})
	if off.Err == nil || !off.Warn || off.Hint == "" {
		t.Errorf("without a heartbeat: %+v, want a warning with a hint", off)
	}
	on := heartbeatFinding(&config.Config{Heartbeat: config.Heartbeat{URL: "https://hc-ping.com/x", Every: 5 * time.Minute}})
	if on.Err != nil || !strings.Contains(on.Info, "5m0s") {
		t.Errorf("with a heartbeat: %+v", on)
	}
}

func TestAPIFindingWarnsOnlyWhenOtherMachinesCanReachIt(t *testing.T) {
	local := apiFinding(&config.Config{API: config.API{Listen: "127.0.0.1:9311"}})
	if local.Err != nil || !strings.Contains(local.Info, "this machine only") {
		t.Errorf("loopback: %+v", local)
	}
	open := apiFinding(&config.Config{API: config.API{Listen: "0.0.0.0:9311"}})
	if open.Err == nil || !open.Warn || open.Hint == "" {
		t.Errorf("all interfaces: %+v, want a warning with a hint", open)
	}
}

// The repositories are checked at the same time: one that is slow to answer must not make the ones after it wait.
func TestRepositoriesAreCheckedAtTheSameTimeAndReportedInOrder(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "restic")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repos := map[string]config.Repository{}
	for _, name := range []string{"d", "a", "c", "b"} {
		repos[name] = config.Repository{URL: "/r/" + name, Password: "x"}
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	began := time.Now()
	findings := repositoryFindings(context.Background(), &config.Config{Repositories: repos})
	if took := time.Since(began); took > 2500*time.Millisecond {
		t.Errorf("four repositories of one second each took %v: they are checked one after the other", took)
	}
	var names []string
	for _, finding := range findings {
		names = append(names, finding.Name)
		if finding.Err != nil {
			t.Errorf("%s: %v", finding.Name, finding.Err)
		}
	}
	if want := "repository a,repository b,repository c,repository d"; strings.Join(names, ",") != want {
		t.Errorf("order = %v", names)
	}
}

// writeAPICertificate saves a self-signed certificate that ends at notAfter and its key, and returns
// the settings that point at them.
func writeAPICertificate(t *testing.T, notAfter time.Time, keyMode os.FileMode) config.API {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sentinel-test"}, NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	settings := config.API{Listen: "0.0.0.0:9311", TLSCert: filepath.Join(dir, "cert.pem"), TLSKey: filepath.Join(dir, "key.pem")}
	if err := os.WriteFile(settings.TLSCert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.TLSKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), keyMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings.TLSKey, keyMode); err != nil { // WriteFile is bound by the umask
		t.Fatal(err)
	}
	return settings
}

func TestAPIFindingChecksTheCertificateInsteadOfWarningAboutPlainHTTP(t *testing.T) {
	now := time.Now()
	tests := map[string]struct {
		notAfter time.Time
		keyMode  os.FileMode
		want     string // "ok", "warning" or "failed"
	}{
		"valid for months":       {now.Add(90 * 24 * time.Hour), 0o600, "ok"},
		"expires in a week":      {now.Add(7 * 24 * time.Hour), 0o600, "warning"},
		"expired yesterday":      {now.Add(-24 * time.Hour), 0o600, "failed"},
		"key others can read":    {now.Add(90 * 24 * time.Hour), 0o644, "warning"},
		"key only the group can": {now.Add(90 * 24 * time.Hour), 0o640, "warning"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			finding := apiFinding(&config.Config{API: writeAPICertificate(t, tc.notAfter, tc.keyMode)})
			got := "ok"
			switch {
			case finding.Err != nil && finding.Warn:
				got = "warning"
			case finding.Err != nil:
				got = "failed"
			}
			if got != tc.want {
				t.Errorf("finding = %+v, want %s", finding, tc.want)
			}
			if finding.Err != nil && finding.Hint == "" {
				t.Error("a problem needs a hint")
			}
		})
	}
	if ok := apiFinding(&config.Config{API: writeAPICertificate(t, now.Add(90*24*time.Hour), 0o600)}); !strings.Contains(ok.Info, "HTTPS") {
		t.Errorf("info = %q", ok.Info)
	}
}

func TestAPIFindingFailsForFilesThatCannotBeLoaded(t *testing.T) {
	missing := config.API{Listen: "0.0.0.0:9311", TLSCert: "/nowhere/cert.pem", TLSKey: "/nowhere/key.pem"}
	if finding := apiFinding(&config.Config{API: missing}); finding.Err == nil || finding.Warn || !strings.Contains(finding.Err.Error(), "cannot be loaded") {
		t.Errorf("finding = %+v, want a failure, as the daemon would not start", finding)
	}
}
