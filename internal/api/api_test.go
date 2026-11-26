package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sentinel/internal/app"
	"sentinel/internal/backup"
	"sentinel/internal/doctor"
	"sentinel/internal/restic"
	"sentinel/internal/status"
)

const testToken = "s3cret-token"

// fakeBackend answers from fields, and fails with err when it is set.
type fakeBackend struct {
	err      error
	started  []string
	stopped  []string
	findings []doctor.Finding
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

// call sends a request with the token (unless token is "") and returns the response.
func call(t *testing.T, backend Backend, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	New(backend, testToken).Handler().ServeHTTP(rec, req)
	return rec
}

func TestEveryAPIRouteNeedsTheTokenButHealthz(t *testing.T) {
	routes := []string{
		"GET /api/v1/status", "GET /api/v1/alerts", "GET /api/v1/jobs", "GET /api/v1/jobs/shop/snapshots?repository=local",
		"POST /api/v1/jobs/shop/backups", "DELETE /api/v1/jobs/shop/backups/running", "GET /api/v1/doctor",
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

func TestServeStopsWhenItsContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, "127.0.0.1:0", http.NotFoundHandler()) }()
	time.Sleep(100 * time.Millisecond)
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
