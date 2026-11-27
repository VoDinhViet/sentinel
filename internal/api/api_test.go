package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel/internal/app"
	"sentinel/internal/backup"
	"sentinel/internal/doctor"
	"sentinel/internal/host"
	"sentinel/internal/restic"
	"sentinel/internal/status"
)

const (
	testToken     = "s3cret-token"
	testReadToken = "r3ad-only-token"
)

// fakeBackend answers from fields, and fails with err when it is set.
type fakeBackend struct {
	err        error
	started    []string
	stopped    []string
	findings   []doctor.Finding
	hostReport app.HostReport
}

func (f *fakeBackend) Status(context.Context, time.Time) (status.Report, error) {
	if f.err != nil {
		return status.Report{}, f.err
	}
	return status.Report{Server: "web-01", Alerts: []status.Alert{{Key: "stale:shop", Message: "no backup for 3 days"}}}, nil
}

func (f *fakeBackend) JobRepositories() []app.JobRepository {
	return []app.JobRepository{{Job: "shop", Repository: "local"}, {Job: "shop", Repository: "s3"}}
}

func (f *fakeBackend) ListSnapshots(_ context.Context, jobName, repo string) ([]restic.Snapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []restic.Snapshot{{ID: "abc123", ShortID: "abc1", Tags: []string{"job=" + jobName}}}, nil
}

func (f *fakeBackend) StartBackup(jobName, repo string) (int, string, error) {
	if f.err != nil {
		return 0, "", f.err
	}
	f.started = append(f.started, jobName+"/"+repo)
	return 4242, "/state/logs/backup-" + jobName + ".log", nil
}

func (f *fakeBackend) RequestStop(jobName string) error {
	if f.err != nil {
		return f.err
	}
	f.stopped = append(f.stopped, jobName)
	return nil
}

func (f *fakeBackend) Doctor(context.Context) []doctor.Finding { return f.findings }

func (f *fakeBackend) Host(context.Context) app.HostReport { return f.hostReport }

// call sends a request with the token (unless token is "") and returns the response.
func call(t *testing.T, backend Backend, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	New(backend, Options{Token: testToken, ReadToken: testReadToken}).Handler().ServeHTTP(rec, req)
	return rec
}

func TestEveryAPIRouteNeedsTheTokenButHealthz(t *testing.T) {
	routes := []string{
		"GET /api/v1/status", "GET /api/v1/alerts", "GET /api/v1/jobs", "GET /api/v1/jobs/shop/snapshots?repository=local",
		"POST /api/v1/jobs/shop/backups", "DELETE /api/v1/jobs/shop/backups/running", "GET /api/v1/doctor", "GET /api/v1/host",
	}
	for _, route := range routes {
		method, target, _ := strings.Cut(route, " ")
		for name, token := range map[string]string{"no token": "", "wrong token": "nope"} {
			t.Run(route+" "+name, func(t *testing.T) {
				rec := call(t, &fakeBackend{}, method, target, token)
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("status = %d, want 401", rec.Code)
				}
			})
		}
	}
	if rec := call(t, &fakeBackend{}, "GET", "/healthz", ""); rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", rec.Code)
	}
}

func TestTheReadTokenOnlyReads(t *testing.T) {
	routes := []struct {
		method, target string
		writes         bool
	}{
		{"GET", "/api/v1/status", false}, {"GET", "/api/v1/alerts", false}, {"GET", "/api/v1/jobs", false},
		{"GET", "/api/v1/jobs/shop/snapshots?repository=local", false}, {"GET", "/api/v1/doctor", false}, {"GET", "/api/v1/host", false},
		{"POST", "/api/v1/jobs/shop/backups", true}, {"DELETE", "/api/v1/jobs/shop/backups/running", true},
	}
	for _, route := range routes {
		for name, token := range map[string]string{"token": testToken, "read token": testReadToken} {
			t.Run(route.method+" "+route.target+" "+name, func(t *testing.T) {
				backend := &fakeBackend{}
				rec := call(t, backend, route.method, route.target, token)
				mustBeDenied := route.writes && token == testReadToken
				switch {
				case mustBeDenied && rec.Code != http.StatusForbidden:
					t.Errorf("status = %d, want 403", rec.Code)
				case !mustBeDenied && (rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized):
					t.Errorf("status = %d, the token must be accepted", rec.Code)
				}
				if mustBeDenied && len(backend.started)+len(backend.stopped) != 0 {
					t.Errorf("the read token changed something: started %v, stopped %v", backend.started, backend.stopped)
				}
			})
		}
	}
}

func TestTheReadTokenIsRefusedWhenNoneIsSet(t *testing.T) {
	for _, readToken := range []string{"", testReadToken} {
		req := httptest.NewRequest("GET", "/api/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+testReadToken)
		rec := httptest.NewRecorder()
		New(&fakeBackend{}, Options{Token: testToken, ReadToken: readToken}).Handler().ServeHTTP(rec, req)
		wantRefused := readToken == ""
		if refused := rec.Code == http.StatusUnauthorized; refused != wantRefused {
			t.Errorf("read token %q configured: status = %d, refused = %v, want %v", readToken, rec.Code, refused, wantRefused)
		}
	}
}

func TestStatusIsTheJSONOfTheStatusCommand(t *testing.T) {
	rec := call(t, &fakeBackend{}, "GET", "/api/v1/status", testToken)
	var report status.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("code %d, err %v, body %s", rec.Code, err, rec.Body)
	}
	if report.Server != "web-01" || len(report.Alerts) != 1 {
		t.Errorf("report = %+v", report)
	}
}

func TestStatusCarriesTheVersionOfTheBinary(t *testing.T) {
	for _, version := range []string{"v1.2.3", ""} {
		t.Run("version "+version, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/status", nil)
			req.Header.Set("Authorization", "Bearer "+testToken)
			rec := httptest.NewRecorder()
			New(&fakeBackend{}, Options{Token: testToken, Version: version}).Handler().ServeHTTP(rec, req)
			var report status.Report
			if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil || report.Version != version {
				t.Errorf("version = %q, err %v, want %q (body %s)", report.Version, err, version, rec.Body)
			}
			if version == "" && strings.Contains(rec.Body.String(), `"version"`) {
				t.Errorf("an unset version is in the report: %s", rec.Body)
			}
		})
	}
}

func TestAlertsAreAnEmptyListNotNull(t *testing.T) {
	rec := call(t, &quietBackend{}, "GET", "/api/v1/alerts", testToken)
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %s, want []", got)
	}
}

// quietBackend has no alerts.
type quietBackend struct{ fakeBackend }

func (*quietBackend) Status(context.Context, time.Time) (status.Report, error) {
	return status.Report{}, nil
}

func TestJobsListsEveryJobAndRepository(t *testing.T) {
	rec := call(t, &fakeBackend{}, "GET", "/api/v1/jobs", testToken)
	var entries []jobEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil || len(entries) != 2 || entries[1].Repository != "s3" {
		t.Errorf("entries = %+v, err %v", entries, err)
	}
}

func TestSnapshotsNeedTheRepository(t *testing.T) {
	if rec := call(t, &fakeBackend{}, "GET", "/api/v1/jobs/shop/snapshots", testToken); rec.Code != http.StatusBadRequest {
		t.Errorf("without repository: status = %d, want 400", rec.Code)
	}
	rec := call(t, &fakeBackend{}, "GET", "/api/v1/jobs/shop/snapshots?repository=local", testToken)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "job=shop") {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestStartBackupAnswers202WithThePid(t *testing.T) {
	backend := &fakeBackend{}
	rec := call(t, backend, "POST", "/api/v1/jobs/shop/backups?repository=s3", testToken)
	var started startedBackup
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || rec.Code != http.StatusAccepted || started.PID != 4242 {
		t.Fatalf("status %d, body %s, err %v", rec.Code, rec.Body, err)
	}
	if len(backend.started) != 1 || backend.started[0] != "shop/s3" {
		t.Errorf("started = %v", backend.started)
	}
}

func TestStopBackupAsksTheBackendToStop(t *testing.T) {
	backend := &fakeBackend{}
	rec := call(t, backend, "DELETE", "/api/v1/jobs/shop/backups/running", testToken)
	if rec.Code != http.StatusAccepted || len(backend.stopped) != 1 || backend.stopped[0] != "shop" {
		t.Errorf("status %d, stopped %v", rec.Code, backend.stopped)
	}
}

func TestErrorsMapToStatusCodes(t *testing.T) {
	tests := map[string]struct {
		err  error
		want int
	}{
		"unknown job":     {fmt.Errorf("job %q %w", "x", app.ErrNotFound), http.StatusNotFound},
		"already running": {fmt.Errorf("a backup of x is %w (pid 1)", app.ErrRunning), http.StatusConflict},
		"nothing to stop": {fmt.Errorf("%w: %q", backup.ErrNotRunning, "x"), http.StatusConflict},
		"anything else":   {errors.New("boom"), http.StatusInternalServerError},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := call(t, &fakeBackend{err: tc.err}, "POST", "/api/v1/jobs/x/backups", testToken)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

func TestAnInternalErrorNeverReachesTheCaller(t *testing.T) {
	leak := errors.New("open s3:https://backup-user:hunter2@storage.example/repo: connection refused")
	rec := call(t, &fakeBackend{err: leak}, "GET", "/api/v1/status", testToken)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "storage.example") {
		t.Errorf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestDoctorFindingsCarryALevel(t *testing.T) {
	backend := &fakeBackend{findings: []doctor.Finding{
		{Name: "restic", Info: "restic 0.18"},
		{Name: "locale", Err: errors.New("not UTF-8"), Warn: true, Hint: "set LANG"},
		{Name: "store", Err: errors.New("cannot open")},
	}}
	rec := call(t, backend, "GET", "/api/v1/doctor", testToken)
	var findings []finding
	if err := json.Unmarshal(rec.Body.Bytes(), &findings); err != nil || len(findings) != 3 {
		t.Fatalf("findings %+v, err %v", findings, err)
	}
	levels := []string{findings[0].Level, findings[1].Level, findings[2].Level}
	if strings.Join(levels, ",") != "ok,warning,failed" {
		t.Errorf("levels = %v", levels)
	}
}

func TestHostIsTheStateOfTheMachineInPlainJSON(t *testing.T) {
	backend := &fakeBackend{hostReport: app.HostReport{
		State: host.State{
			Hostname: "web-01", OS: "ubuntu 24.04", Kernel: "6.8.0", Uptime: 90 * time.Minute, Load: [3]float64{0.5, 0.4, 0.3}, CPUs: 4, CPUPercent: 12.5,
			Memory:      host.Memory{TotalBytes: 8 << 30, AvailableBytes: 3 << 30, SwapTotalBytes: 2 << 30, SwapFreeBytes: 2 << 30},
			Disks:       []host.Disk{{Mount: "/", Device: "/dev/sda1", FSType: "ext4", FreeBytes: 10 << 30, TotalBytes: 40 << 30}},
			Systemd:     true,
			Sentinel:    host.Unit{Name: "sentinel.service", Active: "active", Description: "Sentinel"},
			FailedUnits: []host.Unit{{Name: "nginx.service", Active: "failed", Description: "web \x1b[31mserver"}},
			Errs:        []error{errors.New("reading swap: unavailable")},
		},
		MinFreePercent: 10,
	}}
	rec := call(t, backend, "GET", "/api/v1/host", testToken)
	var entry hostEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entry); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("code %d, err %v, body %s", rec.Code, err, rec.Body)
	}
	if entry.Hostname != "web-01" || entry.UptimeSeconds != 5400 || entry.CPUs != 4 || entry.CPUPercent != 12.5 || entry.Load[1] != 0.4 {
		t.Errorf("entry = %+v", entry)
	}
	if entry.Memory.TotalBytes != 8<<30 || len(entry.Disks) != 1 || entry.Disks[0].FSType != "ext4" || entry.MinFreePercent != 10 {
		t.Errorf("memory, disks or threshold wrong: %+v", entry)
	}
	if entry.Sentinel == nil || entry.Sentinel.Active != "active" || len(entry.FailedUnits) != 1 || entry.FailedUnits[0].Name != "nginx.service" {
		t.Errorf("units wrong: %+v", entry)
	}
	if len(entry.Errors) != 1 || entry.Errors[0] != "reading swap: unavailable" {
		t.Errorf("errors = %v", entry.Errors)
	}
	if strings.Contains(rec.Body.String(), "\x1b") || strings.Contains(rec.Body.String(), `\u001b`) { // the byte, or its JSON escape
		t.Errorf("a control character of a unit description reached the response: %s", rec.Body)
	}
}

func TestHostListsAreEmptyNotNullAndSentinelNeedsSystemd(t *testing.T) {
	rec := call(t, &fakeBackend{}, "GET", "/api/v1/host", testToken)
	body := rec.Body.String()
	for _, want := range []string{`"disks":[]`, `"failed_units":[]`, `"errors":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("the response lacks %s: %s", want, body)
		}
	}
	if strings.Contains(body, `"sentinel"`) {
		t.Errorf("a machine without systemd reports sentinel.service: %s", body)
	}
}

func TestServeStopsWhenItsContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, http.NotFoundHandler()) }()
	resp, err := http.Get("http://" + ln.Addr().String() + "/anything")
	if err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the server does not answer: %v %v", resp, err)
	}
	resp.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestAServerWithoutATokenRefusesEverything(t *testing.T) {
	for name, header := range map[string]string{"no header": "", "empty bearer": "Bearer ", "any bearer": "Bearer x"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/status", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			New(&fakeBackend{}, Options{}).Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

func TestDoctorHidesCredentialsInAnError(t *testing.T) {
	backend := &fakeBackend{findings: []doctor.Finding{
		{Name: "repository s3", Err: errors.New("open s3:https://backup-user:hunter2@storage.example/repo: refused")},
	}}
	rec := call(t, backend, "GET", "/api/v1/doctor", testToken)
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("the password is in the response: %s", rec.Body)
	}
}
