package server

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"sentinel/internal/api"
	"sentinel/internal/config"
	"sentinel/internal/server/service"
	"sentinel/internal/store"
)

const adminPass = "correct horse battery"

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	svc, err := service.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	hash, _ := bcrypt.GenerateFromPassword([]byte(adminPass), bcrypt.MinCost)
	off := false
	cfg := &config.ServerConfig{OfflineAfter: 3 * time.Minute, SecureCookies: &off}
	cfg.Admin.Username, cfg.Admin.PasswordHash = "admin", string(hash)
	s := New(cfg, svc)
	s.Version = "test"
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts
}

// browser is an HTTP client with cookies that does not follow redirects.
func browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

var csrfRe = regexp.MustCompile(`"X-CSRF-Token": "([^"]+)"`)

func login(t *testing.T, c *http.Client, ts *httptest.Server) string {
	t.Helper()
	resp, err := c.PostForm(ts.URL+"/login", url.Values{"username": {"admin"}, "password": {adminPass}})
	if err != nil || resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/manage" {
		t.Fatalf("login: %v %v", resp.Status, err)
	}
	resp.Body.Close()
	resp, _ = c.Get(ts.URL + "/manage")
	m := csrfRe.FindStringSubmatch(body(t, resp))
	if m == nil {
		t.Fatal("no CSRF token on page")
	}
	return m[1]
}

func TestAgentAPIAuth(t *testing.T) {
	_, ts := newTestServer(t)
	for _, auth := range []string{"", "Bearer ", "Bearer snt_nope", "Basic abc"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/agent/report", strings.NewReader("{}"))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("auth %q: %v %v", auth, resp.Status, err)
		}
		resp.Body.Close()
	}
}

func TestLoginAndCSRF(t *testing.T) {
	s, ts := newTestServer(t)
	c := browser(t)

	resp, _ := c.Get(ts.URL + "/manage")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("anonymous /manage = %s %s", resp.Status, resp.Header.Get("Location"))
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/manage/grid", nil)
	req.Header.Set("HX-Request", "true")
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("HX-Redirect") != "/login" {
		t.Errorf("anonymous htmx = %s", resp.Status)
	}

	resp, _ = c.PostForm(ts.URL+"/login", url.Values{"username": {"admin"}, "password": {"wrong"}})
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body(t, resp), "Wrong username or password") {
		t.Errorf("bad password = %s", resp.Status)
	}

	csrf := login(t, c, ts)
	for _, ck := range c.Jar.Cookies(mustURL(ts.URL)) {
		if ck.Name == sessionCookie && ck.Value == "" {
			t.Error("empty session cookie")
		}
	}

	// Creating an agent needs the CSRF token.
	resp, _ = c.PostForm(ts.URL+"/agents", url.Values{"name": {"web-01"}})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST without CSRF = %s", resp.Status)
	}
	resp, _ = c.PostForm(ts.URL+"/agents", url.Values{"name": {"web-01"}, "csrf_token": {csrf}})
	page := body(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "snt_") || !strings.Contains(page, "managed: true") {
		t.Fatalf("create agent = %s\n%s", resp.Status, page)
	}
	agents, _ := s.Service.ListAgents(context.Background())
	if len(agents) != 1 {
		t.Fatalf("agents = %+v", agents)
	}

	resp, _ = c.Get(ts.URL + "/manage")
	if p := body(t, resp); !strings.Contains(p, "web-01") || !strings.Contains(p, "never connected") {
		t.Errorf("manage page missing agent:\n%s", p)
	}

	// Logout ends the session.
	resp, _ = c.PostForm(ts.URL+"/logout", url.Values{"csrf_token": {csrf}})
	resp.Body.Close()
	resp, _ = c.Get(ts.URL + "/manage")
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("after logout = %s", resp.Status)
	}
}

func TestLoginRateLimit(t *testing.T) {
	_, ts := newTestServer(t)
	c := browser(t)
	var last int
	for i := 0; i < 6; i++ {
		resp, _ := c.PostForm(ts.URL+"/login", url.Values{"username": {"admin"}, "password": {"x"}})
		last = resp.StatusCode
		resp.Body.Close()
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("6th attempt = %d, want 429", last)
	}
}

func TestAgentPagesAndCommands(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()
	a, _, _ := s.Service.CreateAgent(ctx, "db-01")
	at := time.Now().Add(-time.Minute)
	s.Service.Ingest(ctx, a.ID, api.Report{
		Hostname: "db-01.lan",
		Jobs:     []api.Job{{Name: "appdb", Type: "postgres", Repositories: []string{"s3"}}},
		BackupRuns: []store.BackupRun{{ID: 1, Job: "appdb", Repository: "s3", StartedAt: at, FinishedAt: at,
			Success: false, Error: "pg_dump: connection refused"}},
		Metrics: []store.Metrics{{ID: 1, At: at, CPU: 42, Memory: 60, DiskMax: 91}},
	})

	c := browser(t)
	csrf := login(t, c, ts)
	base := ts.URL + "/agents/" + itoa(a.ID)

	resp, _ := c.Get(base)
	page := body(t, resp)
	for _, want := range []string{"db-01.lan", "online", "pg_dump: connection refused", "Backup now"} {
		if !strings.Contains(page, want) {
			t.Errorf("agent page missing %q", want)
		}
	}

	resp, _ = c.Get(base + "/metrics.json")
	if p := body(t, resp); !strings.Contains(p, `"cpu":[42]`) {
		t.Errorf("metrics.json = %s", p)
	}

	post := func(path string, form url.Values) string {
		req, _ := http.NewRequest(http.MethodPost, base+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("HX-Request", "true")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return body(t, resp)
	}

	if p := post("/backup", url.Values{"job": {"appdb"}}); !strings.Contains(p, "Backup appdb") || !strings.Contains(p, "queued") {
		t.Errorf("backup now response:\n%s", p)
	}
	if p := post("/backup", url.Values{"job": {"nope"}}); !strings.Contains(p, `has no backup job`) {
		t.Errorf("unknown job response:\n%s", p)
	}
	if p := post("/snapshots", url.Values{"target": {"appdb|s3"}}); !strings.Contains(p, "Asking the server") {
		t.Errorf("snapshots response:\n%s", p)
	}
	// Restore must be confirmed by typing the job name.
	req, _ := http.NewRequest(http.MethodPost, base+"/restore", strings.NewReader(url.Values{
		"job": {"appdb"}, "repository": {"s3"}, "snapshot": {"abcdef12"}, "confirm": {"oops"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unconfirmed restore = %s", resp.Status)
	}
	resp.Body.Close()
	if p := post("/restore", url.Values{"job": {"appdb"}, "repository": {"s3"}, "snapshot": {"abcdef12"}, "confirm": {"appdb"}}); !strings.Contains(p, "Restore appdb snapshot abcdef12") {
		t.Errorf("restore response:\n%s", p)
	}

	cmds, _ := s.Service.ListCommands(ctx, a.ID, 10)
	if len(cmds) != 3 {
		t.Errorf("queued commands = %d, want 3", len(cmds))
	}

	resp, _ = c.Get(ts.URL + "/agents/999")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing agent = %s", resp.Status)
	}
}

func TestConfigPage(t *testing.T) {
	s, ts := newTestServer(t)
	ctx := context.Background()
	a, _, _ := s.Service.CreateAgent(ctx, "web-01")
	c := browser(t)
	csrf := login(t, c, ts)
	base := ts.URL + "/agents/" + itoa(a.ID) + "/config"

	good := "uptime:\n  - { name: web, type: http, url: \"https://example.com\" }\n"
	resp, _ := c.PostForm(base, url.Values{"yaml": {"uptime:\n  - { name: web, type: smtp }"}, "csrf_token": {csrf}})
	if p := body(t, resp); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(p, "type must be") {
		t.Errorf("invalid save = %s", resp.Status)
	}
	resp, _ = c.PostForm(base, url.Values{"yaml": {strings.ReplaceAll(good, "\n", "\r\n")}, "comment": {"first"}, "csrf_token": {csrf}})
	if resp.StatusCode != http.StatusSeeOther || !strings.HasSuffix(resp.Header.Get("Location"), "saved=1") {
		t.Fatalf("save = %s %s", resp.Status, resp.Header.Get("Location"))
	}
	cur, _, _ := s.Service.CurrentConfig(ctx, a.ID)
	if cur.YAML != good {
		t.Errorf("CRLF not normalised: %q", cur.YAML)
	}
	resp, _ = c.Get(base + "?saved=1")
	if p := body(t, resp); !strings.Contains(p, "Saved as v1") || !strings.Contains(p, "waiting for agent") {
		t.Errorf("config page after save:\n%s", p)
	}

	req, _ := http.NewRequest(http.MethodPost, base+"/validate", strings.NewReader(url.Values{"yaml": {"store: /x"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, _ = c.Do(req)
	if p := body(t, resp); !strings.Contains(p, "can only be set in the agent") {
		t.Errorf("validate = %s", p)
	}
}

func TestStaticAndTemplates(t *testing.T) {
	_, ts := newTestServer(t)
	for _, f := range []string{"htmx.min.js", "uplot.min.js", "app.css", "app.js"} {
		resp, err := http.Get(ts.URL + "/static/" + f)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("static %s = %v %v", f, resp.Status, err)
		}
		resp.Body.Close()
	}
	resp, _ := http.Get(ts.URL + "/login")
	if p := body(t, resp); !strings.Contains(p, "Sign in") || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("login page:\n%s", p)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}
