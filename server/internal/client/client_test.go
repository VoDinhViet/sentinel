package client

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sentinel/pkg/pki"
	"sentinel/server/internal/ctlapi"
)

// serve is an HTTPS server with a certificate of a new CA, and the Config that trusts exactly that CA.
func serve(t *testing.T, handler http.Handler) (*httptest.Server, Config) {
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
	server := httptest.NewUnstartedServer(handler)
	server.TLS = pki.CtlTLSConfig(cert)
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, Config{URL: server.URL, CAFingerprint: ca.Fingerprint()}
}

func TestAnAnswerIsDecodedAndTheTokenIsSent(t *testing.T) {
	var sent atomic.Value
	server, cfg := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Store(r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode([]ctlapi.Agent{{Name: "web\x1b[31m01", Version: "1.0\x1b[2J\n.1", State: "active"}})
	}))
	_ = server
	api, err := New(cfg, "tok")
	if err != nil {
		t.Fatal(err)
	}
	agents, err := api.Agents(t.Context())
	if err != nil || len(agents) != 1 || agents[0].Name != "web01" || agents[0].Version != "1.0 .1" {
		t.Errorf("agents = %+v, err %v (escape sequences must be dropped)", agents, err)
	}
	if sent.Load() != "Bearer tok" {
		t.Errorf("Authorization = %v", sent.Load())
	}
}

func TestA401IsAnEndedSessionExceptAtLogin(t *testing.T) {
	_, cfg := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"wrong password"}`))
	}))
	signedIn, _ := New(cfg, "tok")
	if _, err := signedIn.Agents(t.Context()); !errors.Is(err, ErrSessionEnded) {
		t.Errorf("a signed-in call: err = %v, want ErrSessionEnded", err)
	}
	anonymous, _ := New(cfg, "")
	_, err := anonymous.Login(t.Context(), "x")
	var failed *Error
	if !errors.As(err, &failed) || failed.Status != 401 || failed.Message != "wrong password" {
		t.Errorf("login: err = %v, want the server's words", err)
	}
}

func TestARedirectIsNotFollowedSoTheTokenGoesNowhereElse(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer target.Close()
	_, cfg := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	api, _ := New(cfg, "tok")
	_, err := api.Agents(t.Context())
	var failed *Error
	if !errors.As(err, &failed) || failed.Status != http.StatusFound {
		t.Errorf("err = %v, want the redirect reported", err)
	}
	if reached.Load() {
		t.Error("the client followed the redirect")
	}
}

func TestATooLargeAnswerIsRefused(t *testing.T) {
	_, cfg := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`["` + strings.Repeat("a", maxAnswer) + `"]`))
	}))
	api, _ := New(cfg, "tok")
	if _, err := api.Agents(t.Context()); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("err = %v, want the answer refused", err)
	}
}

func TestOnlyTheServerOfThePinnedCAIsTalkedTo(t *testing.T) {
	_, cfg := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	other, _ := pki.NewCA(time.Now())
	cfg.CAFingerprint = other.Fingerprint()
	api, _ := New(cfg, "tok")
	if _, err := api.Agents(t.Context()); err == nil {
		t.Error("a server of another CA was trusted")
	}
}

func TestConfigValidate(t *testing.T) {
	ca, _ := pki.NewCA(time.Now())
	good := ca.Fingerprint()
	for _, test := range []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"https with a port", Config{"https://127.0.0.1:9321", good}, true},
		{"trailing slash", Config{"https://ctl.example.com/", good}, true},
		{"http", Config{"http://127.0.0.1:9321", good}, false},
		{"a path", Config{"https://127.0.0.1:9321/other", good}, false},
		{"user info", Config{"https://user:x@127.0.0.1", good}, false},
		{"no host", Config{"https://:9321", good}, false},
		{"a query", Config{"https://127.0.0.1?x=1", good}, false},
		{"no fingerprint", Config{"https://127.0.0.1:9321", ""}, false},
		{"a bad fingerprint", Config{"https://127.0.0.1:9321", "sha256:abc"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.cfg.Validate(); (err == nil) != test.ok {
				t.Errorf("err = %v, want ok=%v", err, test.ok)
			}
		})
	}
}

func TestTheSessionFileIsPrivateAndTheConfigRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sentinel")
	if _, err := LoadSession(dir); !errors.Is(err, ErrNoSession) {
		t.Errorf("no file: err = %v", err)
	}
	if err := SaveSession(dir, "token-value"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %o, want 700", info.Mode().Perm())
	}
	if token, err := LoadSession(dir); err != nil || token != "token-value" {
		t.Errorf("token %q, err %v", token, err)
	}
	if err := DeleteSession(dir); err != nil {
		t.Fatal(err)
	}
	if err := DeleteSession(dir); err != nil {
		t.Errorf("deleting twice: %v", err)
	}

	ca, _ := pki.NewCA(time.Now())
	want := Config{URL: "https://127.0.0.1:9321", CAFingerprint: ca.Fingerprint()}
	if err := SaveConfig(dir, want); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadConfig(dir); err != nil || got != want {
		t.Errorf("config %+v, err %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFile), []byte("url: https://127.0.0.1\ntypo: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(dir); err == nil {
		t.Error("a config with an unknown key was accepted")
	}
}
