package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type userRow struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
}

type auditRow struct {
	ID        int64          `json:"id"`
	Username  string         `json:"username"`
	Action    string         `json:"action"`
	AgentName string         `json:"agent_name"`
	Job       string         `json:"job"`
	Outcome   string         `json:"outcome"`
	Details   map[string]any `json:"details"`
}

const userPass = "a long password"

// loginAs creates the user through the admin client and returns a client
// logged in as them, plus their id.
func loginAs(t *testing.T, ts *httptest.Server, admin *client, username, role string) (*client, int64) {
	t.Helper()
	var created userRow
	if st := admin.call("POST", "/api/ui/users", map[string]string{"username": username, "password": userPass, "role": role}, &created); st != http.StatusCreated {
		t.Fatalf("create %s = %d", username, st)
	}
	c := newClient(t, ts)
	var session sessionInfo
	if st := c.call("POST", "/api/ui/login", map[string]string{"username": username, "password": userPass}, &session); st != 200 || session.Role != role || session.Username != username {
		t.Fatalf("login %s = %d %+v", username, st, session)
	}
	c.csrf = session.CSRF
	return c, created.ID
}

func auditEntries(t *testing.T, admin *client, query string) []auditRow {
	t.Helper()
	var entries []auditRow
	if st := admin.call("GET", "/api/ui/audit"+query, nil, &entries); st != 200 {
		t.Fatalf("audit%s = %d", query, st)
	}
	return entries
}

func TestRolesGateEndpoints(t *testing.T) {
	_, ts := newTestServer(t)
	admin := newClient(t, ts)
	admin.login()
	var agent struct {
		Agent struct {
			ID int64 `json:"id"`
		} `json:"agent"`
	}
	if st := admin.call("POST", "/api/ui/agents", map[string]string{"name": "web-01"}, &agent); st != http.StatusCreated {
		t.Fatalf("create agent = %d", st)
	}
	agentPath := "/api/ui/agents/" + itoa(agent.Agent.ID)

	viewer, _ := loginAs(t, ts, admin, "vera", "viewer")
	operator, _ := loginAs(t, ts, admin, "otto", "operator")

	type call struct {
		method, path string
		body         any
	}
	adminOnly := []call{
		{"POST", "/api/ui/agents", map[string]string{"name": "web-02"}},
		{"DELETE", agentPath, nil},
		{"POST", agentPath + "/rotate", nil},
		{"GET", agentPath + "/config", nil},
		{"POST", agentPath + "/config", map[string]string{"yaml": "x"}},
		{"POST", agentPath + "/config/validate", map[string]string{"yaml": "x"}},
		{"POST", agentPath + "/config/rollback", map[string]int{"version": 1}},
		{"GET", agentPath + "/db-jobs", nil},
		{"POST", agentPath + "/db-jobs", map[string]any{}},
		{"PUT", agentPath + "/beszel", map[string]any{}},
		{"GET", "/api/ui/users", nil},
		{"POST", "/api/ui/users", map[string]string{"username": "eve", "password": userPass, "role": "admin"}},
		{"PATCH", "/api/ui/users/1", map[string]string{"role": "admin"}},
		{"GET", "/api/ui/audit", nil},
	}
	operatorOnly := []call{
		{"POST", agentPath + "/backup", map[string]string{"job": "db"}},
		{"POST", agentPath + "/snapshots", map[string]string{"job": "db", "repository": "s3"}},
		{"POST", agentPath + "/restore", map[string]string{"job": "db", "repository": "s3", "snapshot": "abcdef12", "confirm": "db"}},
	}
	readable := []string{"/api/ui/agents", agentPath, "/api/ui/databases", agentPath + "/backups", "/api/ui/session"}

	for _, c := range readable {
		for name, cl := range map[string]*client{"viewer": viewer, "operator": operator} {
			if st, raw := cl.do("GET", c, nil); st != 200 {
				t.Errorf("%s GET %s = %d %s", name, c, st, raw)
			}
		}
	}
	for _, e := range append(append([]call{}, adminOnly...), operatorOnly...) {
		if st, _ := viewer.do(e.method, e.path, e.body); st != http.StatusForbidden {
			t.Errorf("viewer %s %s = %d, want 403", e.method, e.path, st)
		}
	}
	for _, e := range adminOnly {
		if st, _ := operator.do(e.method, e.path, e.body); st != http.StatusForbidden {
			t.Errorf("operator %s %s = %d, want 403", e.method, e.path, st)
		}
	}
	for _, e := range operatorOnly {
		// Allowed through the role gate; the unknown job is then rejected with 400.
		if st, raw := operator.do(e.method, e.path, e.body); st != http.StatusBadRequest {
			t.Errorf("operator %s %s = %d %s, want 400 (past the role check)", e.method, e.path, st, raw)
		}
	}
}

func TestAuditTrail(t *testing.T) {
	_, ts := newTestServer(t)
	admin := newClient(t, ts)
	admin.login()
	viewer, _ := loginAs(t, ts, admin, "vera", "viewer")

	// A failed login is recorded under the name that was tried.
	anon := newClient(t, ts)
	anon.do("POST", "/api/ui/login", map[string]string{"username": "mallory", "password": "guess"})

	var agent struct {
		Agent struct {
			ID int64 `json:"id"`
		} `json:"agent"`
	}
	admin.call("POST", "/api/ui/agents", map[string]string{"name": "web-01"}, &agent)
	viewer.do("POST", "/api/ui/agents", map[string]string{"name": "web-02"})                             // denied
	admin.do("POST", "/api/ui/agents/"+itoa(agent.Agent.ID)+"/backup", map[string]string{"job": "nope"}) // fails: unknown job

	entries := auditEntries(t, admin, "")
	find := func(user, action, outcome string) *auditRow {
		for i := range entries {
			if entries[i].Username == user && entries[i].Action == action && entries[i].Outcome == outcome {
				return &entries[i]
			}
		}
		return nil
	}
	for _, want := range []struct{ user, action, outcome string }{
		{"admin", "auth.login", "ok"},
		{"admin", "user.create", "ok"},
		{"vera", "auth.login", "ok"},
		{"mallory", "auth.login", "failed"},
		{"admin", "agent.create", "ok"},
		{"vera", "agent.create", "denied"},
		{"admin", "backup.run", "failed"},
	} {
		if find(want.user, want.action, want.outcome) == nil {
			t.Errorf("no audit entry %+v in %+v", want, entries)
		}
	}
	if e := find("admin", "backup.run", "failed"); e != nil && (e.AgentName != "web-01" || e.Job != "nope") {
		t.Errorf("backup entry lacks agent/job: %+v", e)
	}

	// Filters and the CSV export.
	if got := auditEntries(t, admin, "?outcome=denied"); len(got) != 1 || got[0].Username != "vera" {
		t.Errorf("denied filter = %+v", got)
	}
	if got := auditEntries(t, admin, "?action=user."); len(got) != 1 {
		t.Errorf("action prefix filter = %+v", got)
	}
	status, raw := admin.do("GET", "/api/ui/audit?format=csv", nil)
	if status != 200 || !strings.HasPrefix(string(raw), "id,time,user,ip,action,") || !strings.Contains(string(raw), "agent.create") {
		t.Errorf("csv = %d %q", status, raw)
	}
}

func TestUserManagementAPI(t *testing.T) {
	_, ts := newTestServer(t)
	admin := newClient(t, ts)
	admin.login()

	for name, body := range map[string]map[string]string{
		"bootstrap admin name": {"username": "Admin", "password": userPass, "role": "viewer"},
		"short password":       {"username": "carol", "password": "short", "role": "viewer"},
		"bad role":             {"username": "carol", "password": userPass, "role": "root"},
	} {
		if st, _ := admin.do("POST", "/api/ui/users", body); st < 400 {
			t.Errorf("%s accepted (%d)", name, st)
		}
	}
	bob, bobID := loginAs(t, ts, admin, "bob", "operator")
	if st, _ := admin.do("POST", "/api/ui/users", map[string]string{"username": "BOB", "password": userPass, "role": "viewer"}); st != http.StatusConflict {
		t.Errorf("duplicate username = %d", st)
	}

	var users []struct {
		userRow
		Bootstrap bool `json:"bootstrap"`
	}
	admin.call("GET", "/api/ui/users", nil, &users)
	if len(users) != 2 || !users[0].Bootstrap || users[0].Username != "admin" || users[1].Username != "bob" {
		t.Fatalf("users = %+v", users)
	}

	// Demotion applies to the open session at once.
	if st, _ := bob.do("POST", "/api/ui/agents", map[string]string{"name": "a-1"}); st != http.StatusForbidden {
		t.Fatalf("operator created an agent: %d", st)
	}
	if st, _ := admin.do("PATCH", "/api/ui/users/"+itoa(bobID), map[string]string{"role": "admin"}); st != 200 {
		t.Fatalf("promote = %d", st)
	}
	if st, _ := bob.do("POST", "/api/ui/agents", map[string]string{"name": "a-1"}); st != http.StatusCreated {
		t.Errorf("promoted user cannot create agent: %d", st)
	}

	// Disabling signs the user out.
	if st, _ := admin.do("PATCH", "/api/ui/users/"+itoa(bobID), map[string]any{"disabled": true}); st != 200 {
		t.Fatalf("disable = %d", st)
	}
	if st, _ := bob.do("GET", "/api/ui/agents", nil); st != http.StatusUnauthorized {
		t.Errorf("disabled user still logged in: %d", st)
	}
	again := newClient(t, ts)
	if st, _ := again.do("POST", "/api/ui/login", map[string]string{"username": "bob", "password": userPass}); st != http.StatusUnauthorized {
		t.Errorf("disabled user can log in: %d", st)
	}

	// Passwords never reach the audit log.
	admin.do("PATCH", "/api/ui/users/"+itoa(bobID), map[string]string{"password": "brand new password"})
	st, raw := admin.do("GET", "/api/ui/audit?format=csv", nil)
	if st != 200 || strings.Contains(string(raw), userPass) || strings.Contains(string(raw), "brand new password") {
		t.Errorf("password leaked into the audit log (%d): %s", st, raw)
	}
	if st, _ := admin.do("PATCH", "/api/ui/users/9999", map[string]string{"role": "viewer"}); st != http.StatusNotFound {
		t.Errorf("unknown user = %d", st)
	}
}

func TestUserEndpointsNeedLogin(t *testing.T) {
	_, ts := newTestServer(t)
	c := newClient(t, ts)
	for _, e := range []struct{ method, path string }{
		{"GET", "/api/ui/users"}, {"POST", "/api/ui/users"}, {"PATCH", "/api/ui/users/1"}, {"GET", "/api/ui/audit"},
	} {
		if st, _ := c.do(e.method, e.path, nil); st != http.StatusUnauthorized {
			t.Errorf("%s %s without login = %d", e.method, e.path, st)
		}
	}
}
