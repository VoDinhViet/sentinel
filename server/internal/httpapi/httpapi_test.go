package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/notify"
	"sentinel/pkg/pki"
	"sentinel/server/internal/app"
	"sentinel/server/internal/auth"
	"sentinel/server/internal/config"
	"sentinel/server/internal/ctlapi"
)

const loginPassword = "correct horse battery"

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// plant is a control plane on a new database, listening for agents and for sentinelctl.
type plant struct {
	t      *testing.T
	app    *app.App
	agents *httptest.Server
	ctl    *httptest.Server
}

func newPlant(t *testing.T) *plant {
	t.Helper()
	cfg := config.Default()
	cfg.StateDir = t.TempDir() + "/state"
	cfg.Hosts = []string{"127.0.0.1"}
	if _, err := app.Init(context.Background(), &cfg, loginPassword); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(context.Background(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	cert, err := tls.LoadX509KeyPair(cfg.Files().ServerCert(), cfg.Files().ServerKey())
	if err != nil {
		t.Fatal(err)
	}
	server := New(a)
	p := &plant{t: t, app: a}
	p.agents = httptest.NewUnstartedServer(server.AgentHandler())
	p.agents.TLS = pki.ServerTLSConfig(cert, a.CA.Pool())
	p.agents.StartTLS()
	p.ctl = httptest.NewUnstartedServer(server.CtlHandler())
	p.ctl.TLS = pki.CtlTLSConfig(cert)
	p.ctl.StartTLS()
	t.Cleanup(p.agents.Close)
	t.Cleanup(p.ctl.Close)
	return p
}

// client trusts the CA of the plant, and shows cert when it is not nil.
func (p *plant) client(cert *tls.Certificate) *http.Client {
	return &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{TLSClientConfig: pki.ClientTLSConfig(p.app.CA.Pool(), cert)},
	}
}

// call sends a request and decodes a JSON answer into target (when it is not nil). It returns the status.
func call(t *testing.T, client *http.Client, method, url, token string, body, target any) (int, http.Header) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	if target != nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated) {
		if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
			t.Fatalf("%s %s: decode: %v", method, url, err)
		}
	}
	return resp.StatusCode, resp.Header
}

func (p *plant) login(password string) (int, string) {
	p.t.Helper()
	var answer ctlapi.LoginResponse
	status, _ := call(p.t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteLogin, "", ctlapi.LoginRequest{Password: password}, &answer)
	return status, answer.Token
}

func (p *plant) signedIn() string {
	p.t.Helper()
	status, token := p.login(loginPassword)
	if status != http.StatusOK || token == "" {
		p.t.Fatalf("login: status %d", status)
	}
	return token
}

// join adds the agent name and joins it, returning its certificate.
func (p *plant) join(token, name string) tls.Certificate {
	p.t.Helper()
	var added ctlapi.AddAgentResponse
	if status, _ := call(p.t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteAgents, token, ctlapi.AddAgentRequest{Name: name}, &added); status != http.StatusOK {
		p.t.Fatalf("add %s: status %d", name, status)
	}
	key, csr, err := pki.NewKeyAndCSR()
	if err != nil {
		p.t.Fatal(err)
	}
	var joined agentapi.JoinResponse
	status, _ := call(p.t, p.client(nil), "POST", p.agents.URL+agentapi.RouteJoin, "", agentapi.JoinRequest{Token: added.Token, CSR: string(csr)}, &joined)
	if status != http.StatusOK || joined.Name != name {
		p.t.Fatalf("join %s: status %d, answer %+v", name, status, joined)
	}
	cert, err := tls.X509KeyPair([]byte(joined.Cert), key)
	if err != nil {
		p.t.Fatal(err)
	}
	return cert
}

func TestAnAgentJoinsAndIsKnownByItsCertificate(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	cert := p.join(token, "web01")

	var who agentapi.WhoAmI
	if status, _ := call(t, p.client(&cert), "GET", p.agents.URL+agentapi.RouteWhoAmI, "", nil, &who); status != http.StatusOK || who.Name != "web01" {
		t.Fatalf("whoami: status %d, %+v", status, who)
	}
	var agents []ctlapi.Agent
	call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteAgents, token, nil, &agents)
	if len(agents) != 1 || agents[0].Name != "web01" || agents[0].State != "active" || agents[0].CertNotAfter == nil || agents[0].EnrolledAt == nil {
		t.Errorf("agents = %+v", agents)
	}
}

func TestAJoinTokenWorksOnceAndABadRequestDoesNotSpendIt(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	var added ctlapi.AddAgentResponse
	call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteAgents, token, ctlapi.AddAgentRequest{Name: "db01"}, &added)
	url := p.agents.URL + agentapi.RouteJoin

	if status, _ := call(t, p.client(nil), "POST", url, "", agentapi.JoinRequest{Token: added.Token, CSR: "not a request"}, nil); status != http.StatusBadRequest {
		t.Errorf("a bad certificate request: status %d, want 400", status)
	}
	_, csr, _ := pki.NewKeyAndCSR()
	good := agentapi.JoinRequest{Token: added.Token, CSR: string(csr)}
	if status, _ := call(t, p.client(nil), "POST", url, "", good, nil); status != http.StatusOK {
		t.Errorf("the token after a bad request: status %d, want 200", status)
	}
	if status, _ := call(t, p.client(nil), "POST", url, "", good, nil); status != http.StatusForbidden {
		t.Errorf("the token used twice: status %d, want 403", status)
	}
	if status, _ := call(t, p.client(nil), "POST", url, "", agentapi.JoinRequest{Token: "nonsense", CSR: string(csr)}, nil); status != http.StatusForbidden {
		t.Errorf("an unknown token: status %d, want 403", status)
	}
}

func TestJoinRefusesAnUnknownFieldAnAnythingAfterAndALargeBody(t *testing.T) {
	p := newPlant(t)
	url := p.agents.URL + agentapi.RouteJoin
	for name, body := range map[string]string{
		"unknown field": `{"token":"x","csr":"y","name":"someone"}`,
		"trailing data": `{"token":"x","csr":"y"} {}`,
		"not json":      `token=x`,
	} {
		resp, err := p.client(nil).Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, resp.StatusCode)
		}
	}
	resp, err := p.client(nil).Post(url, "application/json", strings.NewReader(`{"token":"`+strings.Repeat("a", maxJoinBody)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a large body: status %d, want 413", resp.StatusCode)
	}
}

func TestAgentRoutesNeedACertificateThatIsStillAccepted(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	cert := p.join(token, "web01")
	url := p.agents.URL + agentapi.RouteWhoAmI

	if status, _ := call(t, p.client(nil), "GET", url, "", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("no certificate: status %d, want 401", status)
	}
	if status, _ := call(t, p.client(nil), "DELETE", p.ctl.URL+"/v1/ctl/agents/web01", token, nil, nil); status != http.StatusNoContent {
		t.Fatalf("revoke: status %d", status)
	}
	if status, _ := call(t, p.client(&cert), "GET", url, "", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("a revoked agent: status %d, want 401", status)
	}
}

func TestACertificateFromAnotherCAIsNotAccepted(t *testing.T) {
	p := newPlant(t)
	other, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key, csr, _ := pki.NewKeyAndCSR()
	issued, err := other.SignAgent(csr, "web01", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(issued.CertPEM, key)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.client(&cert).Get(p.agents.URL + agentapi.RouteWhoAmI)
	if err == nil {
		resp.Body.Close()
		t.Errorf("a certificate of another CA got status %d, want a failed handshake", resp.StatusCode)
	}
}

func TestTheServerCertificateIsNotAnAgentCertificate(t *testing.T) {
	p := newPlant(t)
	cfg := p.app.Config
	serverCert, err := tls.LoadX509KeyPair(cfg.Files().ServerCert(), cfg.Files().ServerKey())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.client(&serverCert).Get(p.agents.URL + agentapi.RouteWhoAmI)
	if err == nil {
		resp.Body.Close()
		t.Errorf("the server's certificate used as a client got status %d, want a refusal", resp.StatusCode)
	}
}

func TestEachListenerHasOnlyItsOwnRoutes(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	if status, _ := call(t, p.client(nil), "GET", p.agents.URL+ctlapi.RouteAgents, token, nil, nil); status != http.StatusNotFound {
		t.Errorf("a sentinelctl route on the agent port: status %d, want 404", status)
	}
	if status, _ := call(t, p.client(nil), "POST", p.ctl.URL+agentapi.RouteJoin, "", agentapi.JoinRequest{}, nil); status != http.StatusNotFound {
		t.Errorf("join on the sentinelctl port: status %d, want 404", status)
	}
}

func TestCtlRoutesNeedASession(t *testing.T) {
	p := newPlant(t)
	for _, route := range []struct{ method, path string }{
		{"GET", ctlapi.RouteAgents}, {"POST", ctlapi.RouteAgents}, {"DELETE", "/v1/ctl/agents/x"},
		{"GET", ctlapi.RouteAudit}, {"GET", ctlapi.RouteAuditVerify}, {"POST", ctlapi.RouteLogout}, {"POST", ctlapi.RoutePasswd},
	} {
		for _, token := range []string{"", "not-a-session"} {
			if status, _ := call(t, p.client(nil), route.method, p.ctl.URL+route.path, token, nil, nil); status != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q: status %d, want 401", route.method, route.path, token, status)
			}
		}
	}
}

func TestLoginLogoutAndTheBlockAfterFiveWrongPasswords(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	if status, _ := call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteLogout, token, nil, nil); status != http.StatusNoContent {
		t.Fatalf("logout: status %d", status)
	}
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteAgents, token, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("a session after logout: status %d, want 401", status)
	}
	for attempt := 1; attempt < auth.MaxFailures; attempt++ {
		if status, _ := p.login("not the password"); status != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: status %d, want 401", attempt, status)
		}
	}
	status, header := call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteLogin, "", ctlapi.LoginRequest{Password: "not the password"}, nil)
	if status != http.StatusTooManyRequests || header.Get("Retry-After") == "" {
		t.Errorf("the fifth wrong password: status %d, Retry-After %q, want 429 with one", status, header.Get("Retry-After"))
	}
	if status, _ := p.login(loginPassword); status != http.StatusTooManyRequests {
		t.Errorf("the right password while blocked: status %d, want 429", status)
	}
}

func TestChangingThePasswordEndsEverySession(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	other := p.signedIn()
	const changed = "another long password"
	if status, _ := call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RoutePasswd, token, ctlapi.PasswdRequest{Old: "wrong wrong wrong", New: changed}, nil); status != http.StatusForbidden {
		t.Errorf("a wrong old password: status %d, want 403 (401 means the session ended)", status)
	}
	if status, _ := call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RoutePasswd, token, ctlapi.PasswdRequest{Old: loginPassword, New: "short"}, nil); status != http.StatusBadRequest {
		t.Errorf("a short new password: status %d, want 400", status)
	}
	if status, _ := call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RoutePasswd, token, ctlapi.PasswdRequest{Old: loginPassword, New: changed}, nil); status != http.StatusNoContent {
		t.Fatalf("change: status %d", status)
	}
	for _, session := range []string{token, other} {
		if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteAgents, session, nil, nil); status != http.StatusUnauthorized {
			t.Errorf("a session after the change: status %d, want 401", status)
		}
	}
}

func TestAddAndRevokeAnswerForBadNamesAndMissingAgents(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	add := func(name string) int {
		status, _ := call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteAgents, token, ctlapi.AddAgentRequest{Name: name}, nil)
		return status
	}
	if status := add("bad/name"); status != http.StatusBadRequest {
		t.Errorf("a bad name: status %d, want 400", status)
	}
	if status := add("web01"); status != http.StatusOK {
		t.Fatalf("add: status %d", status)
	}
	if status := add("web01"); status != http.StatusOK {
		t.Errorf("adding a pending agent again gives a new token: status %d, want 200", status)
	}
	p.join(token, "web02")
	if status := add("web02"); status != http.StatusConflict {
		t.Errorf("an agent that has joined: status %d, want 409", status)
	}
	if status, _ := call(t, p.client(nil), "DELETE", p.ctl.URL+"/v1/ctl/agents/nobody", token, nil, nil); status != http.StatusNotFound {
		t.Errorf("revoking nobody: status %d, want 404", status)
	}
}

func TestCtlBodiesAreCapped(t *testing.T) {
	p := newPlant(t)
	resp, err := p.client(nil).Post(p.ctl.URL+ctlapi.RouteLogin, "application/json", strings.NewReader(`{"password":"`+strings.Repeat("a", maxCtlBody)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413", resp.StatusCode)
	}
}

func TestTheAuditLogNamesTheSessionAndKeepsItsChain(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	p.join(token, "web01")
	call(t, p.client(nil), "DELETE", p.ctl.URL+"/v1/ctl/agents/web01", token, nil, nil)

	var records []ctlapi.AuditRecord
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteAudit+"?limit=100", token, nil, &records); status != http.StatusOK {
		t.Fatalf("audit: status %d", status)
	}
	seen := map[string]ctlapi.AuditRecord{}
	for _, record := range records {
		seen[record.Action] = record
	}
	revoke, enroll, join := seen["agent.revoke"], seen["agent.enroll"], seen["agent.join"]
	if revoke.Actor != "ctl" || revoke.Target != "web01" || revoke.Session == "" || revoke.IP != "127.0.0.1" {
		t.Errorf("revoke record = %+v, want ctl, a session and the address", revoke)
	}
	if enroll.Actor != "ctl" || join.Actor != "agent:web01" {
		t.Errorf("enroll by %q, join by %q", enroll.Actor, join.Actor)
	}
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteAudit+"?limit=0", token, nil, nil); status != http.StatusBadRequest {
		t.Errorf("limit 0: status %d, want 400", status)
	}
	var verified ctlapi.AuditVerify
	call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteAuditVerify, token, nil, &verified)
	if verified.Error != "" || verified.Checked < len(records) {
		t.Errorf("verify = %+v", verified)
	}
}

func TestClientIP(t *testing.T) {
	for remote, want := range map[string]string{
		"203.0.113.9:5555":                   "203.0.113.9",
		"[::ffff:203.0.113.9]:5555":          "203.0.113.9",
		"[2001:db8:1:2:aaaa:bbbb:cccc:1]:80": "2001:db8:1:2::/64",
		"[2001:db8:1:2::77]:80":              "2001:db8:1:2::/64",
		"weird":                              "weird",
	} {
		req := &http.Request{RemoteAddr: remote}
		if got := clientIP(req); got != want {
			t.Errorf("clientIP(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestAReportIsKeptCleanedAndShownBySentinelctl(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	cert := p.join(token, "web01")
	agent := p.client(&cert)

	var detail ctlapi.AgentDetail
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+"/v1/ctl/agents/web01", token, nil, &detail); status != http.StatusOK || detail.Report != nil {
		t.Fatalf("before any report: status %d, report %v", status, detail.Report)
	}

	// A field this version does not know (a newer agent) is let through; hostile text is cleaned.
	body := `{"version":"2.0\u001b[31m","future_field":1,"alerts":[{"key":"disk:/","message":"full\u0007","since":"2026-01-01T00:00:00Z"}]}`
	req, _ := http.NewRequest("POST", p.agents.URL+agentapi.RouteReport, strings.NewReader(body))
	resp, err := agent.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var answer agentapi.ReportResponse
	_ = json.NewDecoder(resp.Body).Decode(&answer)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || answer.ReportEverySeconds != agentapi.DefaultReportGap || time.Since(answer.ServerTime) > time.Minute {
		t.Fatalf("report: status %d, answer %+v", resp.StatusCode, answer)
	}

	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+"/v1/ctl/agents/web01", token, nil, &detail); status != http.StatusOK {
		t.Fatalf("show: status %d", status)
	}
	if detail.Report == nil || detail.Report.Version != "2.0" || detail.Report.Alerts[0].Message != "full" {
		t.Errorf("the report was not kept cleaned: %+v", detail.Report)
	}
	if detail.Status != "ONLINE" || detail.LastSeen == nil || detail.Version != "2.0" {
		t.Errorf("agent = %+v: want ONLINE, seen, version 2.0", detail.Agent)
	}
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+"/v1/ctl/agents/ghost", token, nil, nil); status != http.StatusNotFound {
		t.Errorf("an unknown agent: status %d", status)
	}
	big := bytes.Repeat([]byte("x"), maxReportBody+1)
	if status, _ := call(t, agent, "POST", p.agents.URL+agentapi.RouteReport, "", json.RawMessage(`"`+string(big)+`"`), nil); status != http.StatusRequestEntityTooLarge {
		t.Errorf("a huge report: status %d", status)
	}
}

func TestRenewGivesACertificateOnlyNearTheEndAndAnAgentAskingIsSeen(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	cert := p.join(token, "web01")
	_, request, err := pki.NewKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	// The fresh certificate has 90 days: too early.
	if status, _ := call(t, p.client(&cert), "POST", p.agents.URL+agentapi.RouteRenew, "", agentapi.RenewRequest{CSR: string(request)}, nil); status != http.StatusBadRequest {
		t.Errorf("renewing a fresh certificate: status %d, want 400", status)
	}
	if status, _ := call(t, p.client(nil), "POST", p.agents.URL+agentapi.RouteRenew, "", agentapi.RenewRequest{CSR: string(request)}, nil); status != http.StatusUnauthorized {
		t.Errorf("renewing without a certificate: status %d, want 401", status)
	}
}

func TestAlertsListsWhatIsOpen(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	p.app.Alerts.Fire(context.Background(), "agent:web01", notify.Critical, "web01 is offline\x1b[31m", "")
	var open []ctlapi.Alert
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteAlerts, token, nil, &open); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if len(open) != 1 || open[0].Key != "agent:web01" || open[0].Message != "web01 is offline" {
		t.Errorf("alerts = %+v", open)
	}
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteAlerts, "", nil, nil); status != http.StatusUnauthorized {
		t.Errorf("alerts without a session: status %d", status)
	}
}

func TestAnAgentThatCallsInALoopIsSlowedDownAndOthersAreNot(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	loud, quiet := p.join(token, "loud"), p.join(token, "quiet")
	refused := 0
	for range 30 {
		if status, _ := call(t, p.client(&loud), "GET", p.agents.URL+agentapi.RouteWhoAmI, "", nil, nil); status == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused == 0 {
		t.Error("30 calls in a row were all answered")
	}
	if status, _ := call(t, p.client(&quiet), "GET", p.agents.URL+agentapi.RouteWhoAmI, "", nil, nil); status != http.StatusOK {
		t.Errorf("another agent was slowed down too: status %d", status)
	}
}
