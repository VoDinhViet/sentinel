package transport

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/pki"
)

// controlPlane is a stand-in for the real one, made of pkg/pki: it joins an agent called web01 for the
// token "good-token". tamper changes the answer before it is sent.
type controlPlane struct {
	ca     *pki.CA
	server *httptest.Server
	tamper func(answer *agentapi.JoinResponse, csr []byte)

	mu           sync.Mutex
	reports      []agentapi.Report
	reportSerial []string // of the certificate each report was made with
	reportStatus int      // when not 0, the answer to a report
	renewals     int
}

func newControlPlane(t *testing.T) *controlPlane {
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
	plane := &controlPlane{ca: ca}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+agentapi.RouteJoin, plane.join)
	mux.HandleFunc("GET "+agentapi.RouteWhoAmI, plane.whoAmI)
	mux.HandleFunc("POST "+agentapi.RouteReport, plane.report)
	mux.HandleFunc("POST "+agentapi.RouteRenew, plane.renew)
	plane.server = httptest.NewUnstartedServer(mux)
	plane.server.TLS = pki.ServerTLSConfig(cert, ca.Pool())
	plane.server.StartTLS()
	t.Cleanup(plane.server.Close)
	return plane
}

func (p *controlPlane) join(w http.ResponseWriter, r *http.Request) {
	var req agentapi.JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token != "good-token" {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"the join token is not valid"}`))
		return
	}
	issued, err := p.ca.SignAgent([]byte(req.CSR), "web01", time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	answer := agentapi.JoinResponse{Name: "web01", Cert: string(issued.CertPEM), CA: string(p.ca.CertPEM()), NotAfter: issued.NotAfter}
	if p.tamper != nil {
		p.tamper(&answer, []byte(req.CSR))
	}
	_ = json.NewEncoder(w).Encode(answer)
}

func (p *controlPlane) whoAmI(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	leaf := r.TLS.VerifiedChains[0][0]
	name, _ := pki.AgentName(leaf)
	_ = json.NewEncoder(w).Encode(agentapi.WhoAmI{Name: name, NotAfter: leaf.NotAfter})
}

func (p *controlPlane) report(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reportStatus != 0 {
		w.WriteHeader(p.reportStatus)
		_, _ = w.Write([]byte(`{"error":"the certificate is not accepted"}`))
		return
	}
	var report agentapi.Report
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.reports = append(p.reports, report)
	p.reportSerial = append(p.reportSerial, pki.SerialOf(r.TLS.VerifiedChains[0][0]))
	_ = json.NewEncoder(w).Encode(agentapi.ReportResponse{ServerTime: time.Now(), ReportEverySeconds: 60})
}

func (p *controlPlane) renew(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var req agentapi.RenewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name, _ := pki.AgentName(r.TLS.VerifiedChains[0][0])
	issued, err := p.ca.SignAgent([]byte(req.CSR), name, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.renewals++
	_ = json.NewEncoder(w).Encode(agentapi.RenewResponse{Cert: string(issued.CertPEM), NotAfter: issued.NotAfter})
}

func (p *controlPlane) options(t *testing.T) JoinOptions {
	return JoinOptions{URL: p.server.URL, Token: "good-token", CAFingerprint: p.ca.Fingerprint(), Dir: filepath.Join(t.TempDir(), DirName)}
}

func TestJoinWritesTheIdentityAndTheCertificateWorks(t *testing.T) {
	plane := newControlPlane(t)
	opts := plane.options(t)
	joined, err := Join(t.Context(), opts)
	if err != nil || joined.Name != "web01" || joined.NotAfter.Before(time.Now().Add(80*24*time.Hour)) {
		t.Fatalf("joined %+v, err %v", joined, err)
	}
	for file, mode := range map[string]os.FileMode{KeyFile: 0o600, CertFile: 0o644, CAFile: 0o644} {
		info, err := os.Stat(filepath.Join(opts.Dir, file))
		if err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v, %v, want mode %o", file, info, err, mode)
		}
	}
	who, err := WhoAmI(t.Context(), opts.Dir, opts.URL)
	if err != nil || who.Name != "web01" {
		t.Errorf("whoami: %+v, %v", who, err)
	}
}

func TestJoinAcceptsTheFingerprintTheWayAnOperatorPastesIt(t *testing.T) {
	plane := newControlPlane(t)
	opts := plane.options(t)
	opts.CAFingerprint = strings.ToUpper(strings.TrimPrefix(plane.ca.Fingerprint(), "sha256:"))
	if _, err := Join(t.Context(), opts); err != nil {
		t.Errorf("a fingerprint without the prefix, in capitals: %v", err)
	}
}

func TestJoinTrustsOnlyTheCAOfTheFingerprint(t *testing.T) {
	plane := newControlPlane(t)
	opts := plane.options(t)
	other, _ := pki.NewCA(time.Now())
	opts.CAFingerprint = other.Fingerprint()
	if _, err := Join(t.Context(), opts); err == nil {
		t.Error("joined a control plane whose CA is not the one given")
	}
	if _, err := os.Stat(opts.Dir); err == nil {
		t.Error("a refused join left files behind")
	}
}

func TestJoinWithABadTokenIsExplainedAndWritesNothing(t *testing.T) {
	plane := newControlPlane(t)
	opts := plane.options(t)
	opts.Token = "wrong"
	if _, err := Join(t.Context(), opts); !errors.Is(err, ErrJoinRefused) {
		t.Errorf("err = %v, want ErrJoinRefused", err)
	}
	if _, err := os.Stat(opts.Dir); err == nil {
		t.Error("a refused join left files behind")
	}
}

func TestJoinRefusesToReplaceAnIdentityUnlessForced(t *testing.T) {
	plane := newControlPlane(t)
	opts := plane.options(t)
	if _, err := Join(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(opts.Dir, KeyFile))
	if _, err := Join(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("a second join: err = %v, want a hint about --force", err)
	}
	if after, _ := os.ReadFile(filepath.Join(opts.Dir, KeyFile)); string(after) != string(before) {
		t.Error("the key was replaced without --force")
	}
	opts.Force = true
	if _, err := Join(t.Context(), opts); err != nil {
		t.Errorf("a forced join: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(opts.Dir, KeyFile)); string(after) == string(before) {
		t.Error("--force did not replace the key")
	}
}

// A control plane (or somebody in front of it) that answers with something that is not a certificate for
// this agent must not get its answer written down.
func TestJoinChecksTheAnswerItGets(t *testing.T) {
	otherCA, _ := pki.NewCA(time.Now())
	_, strangerCSR, _ := pki.NewKeyAndCSR()
	tests := []struct {
		name   string
		tamper func(plane *controlPlane, answer *agentapi.JoinResponse, csr []byte)
	}{
		{"another CA in the answer", func(_ *controlPlane, a *agentapi.JoinResponse, _ []byte) { a.CA = string(otherCA.CertPEM()) }},
		{"a certificate of another CA", func(_ *controlPlane, a *agentapi.JoinResponse, csr []byte) {
			issued, _ := otherCA.SignAgent(csr, "web01", time.Now())
			a.Cert = string(issued.CertPEM)
		}},
		{"a certificate for another key", func(plane *controlPlane, a *agentapi.JoinResponse, _ []byte) {
			issued, _ := plane.ca.SignAgent(strangerCSR, "web01", time.Now())
			a.Cert = string(issued.CertPEM)
		}},
		{"another name than the certificate's", func(_ *controlPlane, a *agentapi.JoinResponse, _ []byte) { a.Name = "db01" }},
		{"no certificate", func(_ *controlPlane, a *agentapi.JoinResponse, _ []byte) { a.Cert = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plane := newControlPlane(t)
			plane.tamper = func(a *agentapi.JoinResponse, csr []byte) { test.tamper(plane, a, csr) }
			opts := plane.options(t)
			if _, err := Join(t.Context(), opts); err == nil {
				t.Fatal("a bad answer was accepted")
			}
			if _, err := os.Stat(opts.Dir); err == nil {
				t.Error("a bad answer left files behind")
			}
		})
	}
}

func TestParseAddress(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://ctl.example.com:9320": true,
		"https://127.0.0.1:9320/":      true,
		"http://ctl.example.com:9320":  false,
		"https://ctl.example.com/path": false,
		"https://u:p@ctl.example.com":  false,
		"https://":                     false,
		"ctl.example.com:9320":         false,
		"https://ctl.example.com?x=1":  false,
		"https://ctl.example.com#frag": false,
	} {
		if _, err := parseAddress(raw); (err == nil) != ok {
			t.Errorf("parseAddress(%q): err = %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	var reached bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer target.Close()
	plane := newControlPlane(t)
	plane.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	if _, err := Join(t.Context(), plane.options(t)); err == nil {
		t.Error("a redirect was taken for an answer")
	}
	if reached {
		t.Error("the join token was sent to the address of a redirect")
	}
}
