package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sentinel/internal/doctor"
	"sentinel/internal/restic"
	"sentinel/internal/store"
)

// runSentinel runs `sentinel <args>` in process and returns what it wrote to stdout.
func runSentinel(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return out.String(), err
}

// writeConfig saves a config with the given uptime section and returns its path.
func writeConfig(t *testing.T, uptime string) string {
	t.Helper()
	dir := t.TempDir()
	body := fmt.Sprintf(`server_name: web-01
store: %s
repositories:
  local:
    url: %s
    password: pw
jobs:
  - name: documents
    type: files
    paths: [%s]
    repositories: [local]
    schedule: manual
%s`, filepath.Join(dir, "sentinel.db"), filepath.Join(dir, "repo"), dir, uptime)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckOutput(t *testing.T) {
	for _, tc := range []struct {
		output  string
		wantErr bool
	}{
		{"text", false},
		{"json", false},
		{"yaml", true},
		{"", true},
	} {
		if err := checkOutput(tc.output); (err != nil) != tc.wantErr {
			t.Errorf("checkOutput(%q) = %v, wantErr %v", tc.output, err, tc.wantErr)
		}
	}
}

func TestFindingsReport(t *testing.T) {
	for _, tc := range []struct {
		name         string
		findings     []doctor.Finding
		wantOK       bool
		wantStatuses []string
	}{
		{"all ok", []doctor.Finding{{Name: "restic", Info: "0.17"}}, true, []string{"ok"}},
		{"warning keeps ok", []doctor.Finding{{Name: "locale", Err: errors.New("not UTF-8"), Warn: true}}, true, []string{"warn"}},
		{"failure", []doctor.Finding{{Name: "restic"}, {Name: "repo local", Err: errors.New("unreachable"), Hint: "check the url"}}, false, []string{"ok", "fail"}},
		{"none", nil, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := findingsReport(tc.findings)
			if report.OK != tc.wantOK {
				t.Errorf("ok = %v, want %v", report.OK, tc.wantOK)
			}
			if report.Findings == nil {
				t.Fatal("findings must marshal as [], not null")
			}
			var statuses []string
			for _, finding := range report.Findings {
				statuses = append(statuses, finding.Status)
			}
			if strings.Join(statuses, ",") != strings.Join(tc.wantStatuses, ",") {
				t.Errorf("statuses = %v, want %v", statuses, tc.wantStatuses)
			}
		})
	}
}

func TestNewRepoSnaps(t *testing.T) {
	size := int64(2048)
	snaps := []restic.Snapshot{
		{ID: "aaaaaaaaaaaa", ShortID: "aaaaaaaa", Time: time.Date(2026, 11, 23, 1, 0, 0, 0, time.UTC),
			Summary: &struct {
				TotalBytes int64 `json:"total_bytes_processed"`
			}{TotalBytes: size}},
		{ID: "bbbbbbbbbbbb", ShortID: "bbbbbbbb"},
	}
	got := newRepoSnaps("local", snaps, nil)
	if len(got.Snapshots) != 2 || got.Snapshots[0].TotalBytes == nil || *got.Snapshots[0].TotalBytes != size || got.Snapshots[1].TotalBytes != nil {
		t.Fatalf("snapshots = %+v", got.Snapshots)
	}

	failed := newRepoSnaps("s3main", nil, errors.New("unreachable"))
	if failed.Error != "unreachable" || failed.Snapshots == nil || len(failed.Snapshots) != 0 {
		t.Errorf("failed repo = %+v, want error and an empty (non-nil) snapshots list", failed)
	}
}

func TestNewBackupRunReport(t *testing.T) {
	start := time.Date(2026, 11, 23, 1, 0, 0, 0, time.UTC)
	runs := []store.BackupRun{
		{Repository: "local", Success: true, SnapshotID: "abc", BytesAdded: 10, StartedAt: start, FinishedAt: start.Add(90 * time.Second)},
		{Repository: "s3main", Error: "unreachable", StartedAt: start, FinishedAt: start.Add(time.Second)},
	}
	report := newBackupRunReport("documents", runs)
	if report.Job != "documents" || len(report.Runs) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if first := report.Runs[0]; !first.Success || first.Result != "ok" || first.DurationSeconds != 90 || first.SnapshotID != "abc" {
		t.Errorf("first run = %+v", first)
	}
	if second := report.Runs[1]; second.Success || second.Result != "FAILED: unreachable" {
		t.Errorf("second run = %+v", second)
	}
}

func TestCheckCmdJSON(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer site.Close()
	// A listener that is closed straight away leaves a port that refuses connections.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddress := listener.Addr().String()
	listener.Close()

	path := writeConfig(t, fmt.Sprintf(`uptime:
  - name: site
    type: http
    url: %s
    timeout: 2s
  - name: database
    type: tcp
    address: %s
    timeout: 2s
`, site.URL, closedAddress))

	stdout, err := runSentinel(t, "--config", path, "check", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "1 of 2 checks failed") {
		t.Fatalf("err = %v, want 1 of 2 checks failed", err)
	}
	var report checkReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if len(report.Checks) != 2 {
		t.Fatalf("checks = %+v", report.Checks)
	}
	if site := report.Checks[0]; site.Name != "site" || !site.OK || site.Error != "" {
		t.Errorf("site = %+v", site)
	}
	if database := report.Checks[1]; database.Name != "database" || database.OK || database.Error == "" || database.Endpoint != closedAddress {
		t.Errorf("database = %+v", database)
	}
}

func TestCheckCmdJSONWithoutChecks(t *testing.T) {
	stdout, err := runSentinel(t, "--config", writeConfig(t, ""), "check", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string][]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if checks, ok := report["checks"]; !ok || checks == nil || len(checks) != 0 {
		t.Errorf("want \"checks\": [], got %s", stdout)
	}
}

func TestCommandsRejectUnknownOutput(t *testing.T) {
	path := writeConfig(t, "")
	for _, args := range [][]string{
		{"check"}, {"doctor"}, {"status"}, {"backup", "list", "documents"}, {"backup", "run", "documents"},
	} {
		_, err := runSentinel(t, append(append([]string{"--config", path}, args...), "-o", "yaml")...)
		if err == nil || !strings.Contains(err.Error(), "--output") {
			t.Errorf("%v -o yaml: err = %v, want an --output error", args, err)
		}
	}
}

func TestDoctorJSONWithBrokenConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server_name: [broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, err := runSentinel(t, "--config", path, "doctor", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "doctor found problems") {
		t.Fatalf("err = %v, want doctor found problems", err)
	}
	var report doctorReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if report.OK || len(report.Findings) != 1 || report.Findings[0].Status != "fail" || !strings.HasPrefix(report.Findings[0].Name, "config") {
		t.Errorf("report = %+v", report)
	}
}
