package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/fleet"
)

const fleetTestToken = "read-token-0123456789"

// daemonAnswering is a server whose API answers /api/v1/status with body.
func daemonAnswering(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fleetTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeFleetConfig saves a fleet file naming name=url pairs, in order, and returns its path.
func writeFleetConfig(t *testing.T, servers ...[2]string) string {
	t.Helper()
	var content strings.Builder
	content.WriteString("servers:\n")
	for _, server := range servers {
		content.WriteString("  - { name: " + server[0] + ", url: \"" + server[1] + "\", token: " + fleetTestToken + " }\n")
	}
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	healthyReport  = `{"version":"v1.2.0","server":"web-01","alerts":[],"jobs":[{"job":"appdb","repository":"s3","success":true}],"checks":[{"name":"site","ok":true}],"running":[]}`
	troubledReport = `{"version":"v1.1.0","server":"db-01","alerts":[{"key":"stale:shop","message":"no backup for 3 days \u001b[31mred","since":"2026-11-24T10:00:00Z"}],` +
		`"jobs":[{"job":"shop","repository":"s3","success":false}],"checks":[{"name":"db-port","ok":false}],"running":[{"job":"x"}]}`
)

// A fleet of a healthy server, one in trouble and one that is not there.
func threeServers(t *testing.T) string {
	t.Helper()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	return writeFleetConfig(t,
		[2]string{"web-01", daemonAnswering(t, healthyReport).URL},
		[2]string{"db-01", daemonAnswering(t, troubledReport).URL},
		[2]string{"edge", gone.URL},
	)
}

// withoutAnyConfig puts HOME in an empty directory, so no config of Sentinel is found.
func withoutAnyConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SENTINEL_CONFIG", "")
}

func TestFleetStatusListsEveryServerInOrderWithoutAConfig(t *testing.T) {
	withoutAnyConfig(t)
	output, err := runSentinel(t, "fleet", "status", "--fleet-config", threeServers(t))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 4 {
		t.Fatalf("want a header and 3 servers, got:\n%s", output)
	}
	for i, want := range [][]string{{"web-01", "OK", "v1.2.0"}, {"db-01", "ALERT", "v1.1.0"}, {"edge", "UNREACHABLE", "-"}} {
		for _, text := range want {
			if !strings.Contains(lines[i+1], text) {
				t.Errorf("line %d lacks %q: %s", i+1, text, lines[i+1])
			}
		}
	}
	if strings.Contains(output, fleetTestToken) {
		t.Error("the output shows the token")
	}
}

func TestFleetStatusJSONCarriesTheReportOfEachServerThatAnswered(t *testing.T) {
	withoutAnyConfig(t)
	output, err := runSentinel(t, "fleet", "status", "-o", "json", "--fleet-config", threeServers(t))
	if err != nil {
		t.Fatal(err)
	}
	var servers []fleetServerJSON
	if err := json.Unmarshal([]byte(output), &servers); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, output)
	}
	if len(servers) != 3 || servers[0].Name != "web-01" || servers[2].Name != "edge" {
		t.Fatalf("servers = %+v", servers)
	}
	if servers[0].Report == nil || servers[0].Report.Version != "v1.2.0" || servers[1].State != fleet.StateAlert {
		t.Errorf("answered servers = %+v, %+v", servers[0], servers[1])
	}
	if servers[2].Report != nil || servers[2].Error == "" || servers[2].State != fleet.StateUnreachable {
		t.Errorf("the unreachable server = %+v", servers[2])
	}
	if strings.Contains(output, fleetTestToken) || strings.Contains(output, `"token"`) {
		t.Error("the JSON shows the token")
	}
}

func TestFleetAlertsGathersTheAlertsAndTheServersThatAreDown(t *testing.T) {
	withoutAnyConfig(t)
	path := threeServers(t)
	output, err := runSentinel(t, "fleet", "alerts", "--fleet-config", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Open alerts: 2", "db-01", "stale:shop", "edge", "unreachable"} {
		if !strings.Contains(output, want) {
			t.Errorf("alerts lack %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "\x1b") {
		t.Error("an escape sequence of the remote server reached the terminal")
	}

	output, err = runSentinel(t, "fleet", "alerts", "-o", "json", "--fleet-config", path)
	if err != nil {
		t.Fatal(err)
	}
	var alerts []fleetAlert
	if err := json.Unmarshal([]byte(output), &alerts); err != nil || len(alerts) != 2 {
		t.Fatalf("alerts = %+v, err %v\n%s", alerts, err, output)
	}
	if alerts[0].Server != "db-01" || alerts[1].Key != unreachableAlertKey {
		t.Errorf("alerts = %+v", alerts)
	}
}

func TestFleetAlertsOfAHealthyFleetIsEmptyNotNull(t *testing.T) {
	withoutAnyConfig(t)
	path := writeFleetConfig(t, [2]string{"web-01", daemonAnswering(t, healthyReport).URL})
	output, err := runSentinel(t, "fleet", "alerts", "-o", "json", "--fleet-config", path)
	if err != nil || strings.TrimSpace(output) != "[]" {
		t.Errorf("output = %q, err = %v, want []", output, err)
	}
}

func TestFleetCommandsExplainAMissingOrBadFile(t *testing.T) {
	withoutAnyConfig(t)
	t.Setenv(fleet.EnvPath, "")
	for _, args := range [][]string{
		{"fleet", "status"}, // no file in the default place
		{"fleet", "alerts", "--fleet-config", filepath.Join(t.TempDir(), "fleet.yaml")},
		{"fleet", "status", "-o", "xml", "--fleet-config", "x"},
	} {
		if _, err := runSentinel(t, args...); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}
