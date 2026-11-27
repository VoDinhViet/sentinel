package doctor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/store"
	"sentinel/agent/internal/transport"
	"sentinel/pkg/notify"
	"sentinel/pkg/pki"
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
	on := heartbeatFinding(&config.Config{Heartbeat: notify.HeartbeatSettings{URL: "https://hc-ping.com/x", Every: 5 * time.Minute}})
	if on.Err != nil || !strings.Contains(on.Info, "5m0s") {
		t.Errorf("with a heartbeat: %+v", on)
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

func TestDeprecatedFindingNamesTheBlocksAndPointsToTheControlPlane(t *testing.T) {
	finding := deprecatedFinding((&config.Config{API: config.API{Listen: "127.0.0.1:9311"}, Fleet: config.Fleet{Config: "/etc/sentinel/fleet.yaml"}}).Deprecated())
	if finding.Err == nil || !finding.Warn || !strings.Contains(finding.Err.Error(), "api, fleet") || !strings.Contains(finding.Hint, "sentinelctl") {
		t.Errorf("finding = %+v, want a warning that names both blocks and points to the control plane", finding)
	}
}

// identityFor writes the files `sentinel agent join` would, for an agent called web01, and returns the CA.
func identityFor(t *testing.T, dir string) *pki.CA {
	t.Helper()
	ca, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key, request, err := pki.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	issued, err := ca.SignAgent(request, "web01", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for file, data := range map[string][]byte{transport.CAFile: ca.CertPEM(), transport.KeyFile: key, transport.CertFile: issued.CertPEM} {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ca
}

func TestControlPlaneFindingFailsWithoutAnIdentityAndWarnsWhenTheControlPlaneIsOutOfReach(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // a port nobody listens on any more answers at once
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "https://" + ln.Addr().String()
	ln.Close()
	cfg := &config.Config{ControlPlane: config.ControlPlane{URL: closedURL, Dir: t.TempDir()}}
	if finding := controlPlaneFinding(t.Context(), cfg); finding.Err == nil || finding.Warn || finding.Hint == "" {
		t.Errorf("without an identity: %+v, want a failure with a hint", finding)
	}
	identityFor(t, cfg.ControlPlane.Dir)
	if finding := controlPlaneFinding(t.Context(), cfg); finding.Err == nil || !finding.Warn {
		t.Errorf("an unreachable control plane: %+v, want a warning", finding)
	}
}
