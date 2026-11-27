package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/transport"
	"sentinel/pkg/agentapi"
	"sentinel/pkg/pki"
)

// controlPlane answers a join for the token "good-token" and whoami, like the real one.
func controlPlane(t *testing.T) (url, fingerprint string) {
	t.Helper()
	ca, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	chain, key, err := ca.IssueServer([]string{"127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(chain, key)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+agentapi.RouteJoin, func(w http.ResponseWriter, r *http.Request) {
		var req agentapi.JoinRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Token != "good-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		issued, err := ca.SignAgent([]byte(req.CSR), "web01", time.Now())
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(agentapi.JoinResponse{Name: "web01", Cert: string(issued.CertPEM), CA: string(ca.CertPEM()), NotAfter: issued.NotAfter})
	})
	mux.HandleFunc("GET "+agentapi.RouteWhoAmI, func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		name, _ := pki.AgentName(r.TLS.VerifiedChains[0][0])
		_ = json.NewEncoder(w).Encode(agentapi.WhoAmI{Name: name})
	})
	server := httptest.NewUnstartedServer(mux)
	server.TLS = pki.ServerTLSConfig(cert, ca.Pool())
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.URL, ca.Fingerprint()
}

func runAgentJoin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"--config", filepath.Join(t.TempDir(), "none.yaml"), "agent", "join"}, args...))
	err := root.ExecuteContext(t.Context())
	return out.String(), err
}

func TestAgentJoinTakesTheTokenFromStdinAndSavesTheIdentity(t *testing.T) {
	url, fingerprint := controlPlane(t)
	dir := filepath.Join(t.TempDir(), "agent")
	out, err := runAgentJoin(t, "good-token\n", "--url", url, "--ca-fingerprint", fingerprint, "--token-stdin", "--dir", dir)
	if err != nil {
		t.Fatalf("join: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"web01"`) {
		t.Errorf("output does not name the agent:\n%s", out)
	}
	if strings.Contains(out, "good-token") {
		t.Errorf("the token was printed:\n%s", out)
	}
	if !strings.Contains(out, "control_plane:") || !strings.Contains(out, url) {
		t.Errorf("the output does not say what to add to the config:\n%s", out)
	}
	for _, file := range []string{transport.KeyFile, transport.CertFile, transport.CAFile} {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Errorf("%s: %v", file, err)
		}
	}
}

func TestAgentJoinRefusesWhatIsMissingOrContradictory(t *testing.T) {
	url, fingerprint := controlPlane(t)
	dir := filepath.Join(t.TempDir(), "agent")
	for name, args := range map[string][]string{
		"no url":            {"--ca-fingerprint", fingerprint, "--token", "good-token"},
		"no fingerprint":    {"--url", url, "--token", "good-token"},
		"two token sources": {"--url", url, "--ca-fingerprint", fingerprint, "--token", "x", "--token-stdin"},
		"no token anywhere": {"--url", url, "--ca-fingerprint", fingerprint},
		"a wrong token":     {"--url", url, "--ca-fingerprint", fingerprint, "--token", "wrong"},
		"another CA pinned": {"--url", url, "--ca-fingerprint", "sha256:" + strings.Repeat("ab", 32), "--token", "good-token"},
	} {
		if _, err := runAgentJoin(t, "", append(args, "--dir", dir)...); err == nil {
			t.Errorf("%s: join ran", name)
		}
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("a refused join left files behind")
	}
}
