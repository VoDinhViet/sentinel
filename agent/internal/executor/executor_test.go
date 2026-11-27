package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/config"
	"sentinel/agent/internal/doctor"
	"sentinel/agent/internal/restic"
	"sentinel/pkg/agentapi"
	"sentinel/pkg/ratelimit"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// fakeBackend records what it was asked and answers what the test set.
type fakeBackend struct {
	calls        []string
	startErr     error
	stopErr      error
	snapshots    []restic.Snapshot
	listErr      error
	findings     []doctor.Finding
	jobs         []app.JobRepository
	doctorPanics bool
}

func (f *fakeBackend) StartBackup(job, repo string) (int, string, error) {
	f.calls = append(f.calls, "start "+job+"/"+repo)
	return 4242, "/var/lib/sentinel/logs/x.log", f.startErr
}

func (f *fakeBackend) RequestStop(job string) error {
	f.calls = append(f.calls, "stop "+job)
	return f.stopErr
}

func (f *fakeBackend) ListSnapshots(_ context.Context, job, repo string) ([]restic.Snapshot, error) {
	f.calls = append(f.calls, "snapshots "+job+"/"+repo)
	return f.snapshots, f.listErr
}

func (f *fakeBackend) JobRepositories() []app.JobRepository { return f.jobs }

func (f *fakeBackend) Doctor(context.Context) []doctor.Finding {
	f.calls = append(f.calls, "doctor")
	if f.doctorPanics {
		panic("a bug in a check")
	}
	return f.findings
}

var allowAll = config.Allow{Backup: true, Doctor: true}

// loose is an executor with no practical rate limit, for the tests that run an action many times.
func loose(backend Backend, allow config.Allow) *Executor {
	e := New(backend, allow)
	e.limit = ratelimit.New(1e6, 1000)
	return e
}

func task(action string, params map[string]string) agentapi.Task {
	return agentapi.Task{ID: 7, Action: action, Params: params}
}

func run(e *Executor, t agentapi.Task) (agentapi.TaskResult, []string) {
	var lines []string
	result := e.Run(context.Background(), t, func(line string) { lines = append(lines, line) })
	return result, lines
}

func TestNothingRunsWhileTheAllowlistIsOff(t *testing.T) {
	for _, action := range agentapi.Actions() {
		backend := &fakeBackend{}
		params := map[string]string{"job": "db"}
		switch {
		case action == agentapi.ActionDoctorRun, strings.HasPrefix(action, "os."):
			params = nil
		case strings.HasPrefix(action, "service."):
			params = map[string]string{"unit": "nginx.service"}
		case action == agentapi.ActionContainerRestart:
			params = map[string]string{"container": "web"}
		}
		result, _ := run(New(backend, config.Allow{}), task(action, params))
		if result.State != agentapi.TaskRefused || !strings.Contains(result.Error, "not allowed") || !strings.Contains(result.Error, "control_plane.allow.") {
			t.Errorf("%s: %+v", action, result)
		}
		if len(backend.calls) != 0 {
			t.Errorf("%s: a refused task still did %v", action, backend.calls)
		}
	}
}

func TestEachKeyOfTheAllowlistOpensOnlyItsActions(t *testing.T) {
	for _, tc := range []struct {
		allow config.Allow
		want  map[string]bool
	}{
		{config.Allow{Backup: true}, map[string]bool{agentapi.ActionBackupStart: true, agentapi.ActionBackupStop: true, agentapi.ActionBackupSnapshots: true}},
		{config.Allow{Doctor: true}, map[string]bool{agentapi.ActionDoctorRun: true}},
		{config.Allow{}, map[string]bool{}},
	} {
		for _, action := range agentapi.Actions() {
			if got := tc.allow.Permits(action, nil); got != tc.want[action] {
				t.Errorf("%+v permits %s = %v", tc.allow, action, got)
			}
		}
	}
	if (config.Allow{}).Permits("shell.run", nil) || allowAll.Permits("service.restart", map[string]string{"unit": "nginx.service"}) {
		t.Error("an action that is not in the table was permitted")
	}
	if got := strings.Join(allowAll.Actions(), " "); got != "backup.snapshots backup.start backup.stop doctor.run" {
		t.Errorf("Actions = %q", got)
	}
}

func TestATaskThatTheTableDoesNotAllowIsRefusedEvenWhenEverythingIsOn(t *testing.T) {
	backend := &fakeBackend{}
	e := New(backend, allowAll)
	for name, bad := range map[string]agentapi.Task{
		"a shell":            task("shell.run", map[string]string{"cmd": "id"}),
		"no job":             task(agentapi.ActionBackupStart, nil),
		"a path":             task(agentapi.ActionBackupStart, map[string]string{"job": "db", "path": "/etc"}),
		"a line break":       task(agentapi.ActionBackupStop, map[string]string{"job": "db\nrm"}),
		"a parameter doctor": task(agentapi.ActionDoctorRun, map[string]string{"x": "y"}),
	} {
		if result, _ := run(e, bad); result.State != agentapi.TaskRefused {
			t.Errorf("%s: %+v", name, result)
		}
	}
	if len(backend.calls) != 0 {
		t.Errorf("a bad task did %v", backend.calls)
	}
}

func TestStartAndStopBackupCallTheServiceWithTheNamesOfTheTask(t *testing.T) {
	backend := &fakeBackend{}
	e := loose(backend, allowAll)
	result, lines := run(e, task(agentapi.ActionBackupStart, map[string]string{"job": "db", "repository": "s3"}))
	if result.State != agentapi.TaskSucceeded || !strings.Contains(result.Output, "pid 4242") || strings.Contains(result.Output, "/var/lib") {
		t.Errorf("start: %+v", result)
	}
	if len(lines) != 2 {
		t.Errorf("start log = %q", lines)
	}
	if result, _ := run(e, task(agentapi.ActionBackupStop, map[string]string{"job": "db"})); result.State != agentapi.TaskSucceeded {
		t.Errorf("stop: %+v", result)
	}
	if got := strings.Join(backend.calls, ", "); got != "start db/s3, stop db" {
		t.Errorf("calls = %q", got)
	}
}

func TestABackupThatCannotStartIsAFailureWithTheReason(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"a job that is not in the config": {fmt.Errorf("job %q %w", "ghost", app.ErrNotFound), "no such job"},
		"a backup already running":        {fmt.Errorf("a backup of db is %w (pid 9)", app.ErrRunning), "already running"},
		"anything else":                   {errors.New("restic is missing\nsecond line"), "restic is missing second line"},
	} {
		result, _ := run(New(&fakeBackend{startErr: tc.err}, allowAll), task(agentapi.ActionBackupStart, map[string]string{"job": "db"}))
		if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, tc.want) || strings.Contains(result.Error, "\n") {
			t.Errorf("%s: %+v", name, result)
		}
	}
	result, _ := run(New(&fakeBackend{stopErr: errors.New("no backup of db is running")}, allowAll), task(agentapi.ActionBackupStop, map[string]string{"job": "db"}))
	if result.State != agentapi.TaskFailed {
		t.Errorf("stop of nothing: %+v", result)
	}
}

func TestSnapshotsDefaultToTheOnlyRepositoryOfTheJob(t *testing.T) {
	snaps := []restic.Snapshot{{ShortID: "abc12345", Time: time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC), Hostname: "web01", Paths: []string{"/var/www", "/etc"}}}
	backend := &fakeBackend{snapshots: snaps, jobs: []app.JobRepository{{Job: "db", Repository: "s3"}, {Job: "files", Repository: "s3"}, {Job: "files", Repository: "nas"}}}
	e := loose(backend, allowAll)
	result, _ := run(e, task(agentapi.ActionBackupSnapshots, map[string]string{"job": "db"}))
	if result.State != agentapi.TaskSucceeded || !strings.Contains(result.Output, "abc12345") || !strings.Contains(result.Output, "/var/www,/etc") {
		t.Errorf("one repository: %+v", result)
	}
	if result, _ := run(e, task(agentapi.ActionBackupSnapshots, map[string]string{"job": "files"})); result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "repository=") {
		t.Errorf("two repositories: %+v", result)
	}
	if result, _ := run(e, task(agentapi.ActionBackupSnapshots, map[string]string{"job": "files", "repository": "nas"})); result.State != agentapi.TaskSucceeded {
		t.Errorf("named repository: %+v", result)
	}
	if result, _ := run(e, task(agentapi.ActionBackupSnapshots, map[string]string{"job": "ghost"})); result.State != agentapi.TaskFailed {
		t.Errorf("no such job: %+v", result)
	}
	if got := strings.Join(backend.calls, ", "); got != "snapshots db/s3, snapshots files/nas" {
		t.Errorf("calls = %q", got)
	}
}

func TestOnlyTheNewestSnapshotsAreListed(t *testing.T) {
	var many []restic.Snapshot
	for i := range maxSnapshots + 10 {
		many = append(many, restic.Snapshot{ShortID: fmt.Sprintf("id%04d", i), Time: time.Now()})
	}
	backend := &fakeBackend{snapshots: many, jobs: []app.JobRepository{{Job: "db", Repository: "s3"}}}
	result, _ := run(New(backend, allowAll), task(agentapi.ActionBackupSnapshots, map[string]string{"job": "db"}))
	if strings.Count(result.Output, "\n") != maxSnapshots || !strings.Contains(result.Output, "and 10 older") {
		t.Errorf("output has %d lines:\n%s", strings.Count(result.Output, "\n")+1, result.Output)
	}
}

func TestDoctorListsEveryFindingAndFailsWhenOneFailed(t *testing.T) {
	backend := &fakeBackend{findings: []doctor.Finding{
		{Name: "restic", Info: "restic 0.17"},
		{Name: "locale", Err: errors.New("not UTF-8"), Warn: true},
		{Name: "repo s3", Err: errors.New("cannot reach\nhttps://user:hunter2@host/bucket")},
	}}
	result, lines := run(New(backend, allowAll), task(agentapi.ActionDoctorRun, nil))
	if result.State != agentapi.TaskFailed || result.Error != "1 of 3 checks failed" {
		t.Errorf("result = %+v", result)
	}
	for _, want := range []string{"ok       restic: restic 0.17", "warning  locale: not UTF-8", "failed   repo s3: cannot reach"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("output lacks %q:\n%s", want, result.Output)
		}
	}
	if strings.Contains(result.Output, "hunter2") || strings.Contains(strings.Join(lines, "\n"), "hunter2") {
		t.Errorf("a password went to the control plane:\n%s\n%q", result.Output, lines)
	}
	ok, _ := run(New(&fakeBackend{findings: []doctor.Finding{{Name: "restic", Info: "ok"}}}, allowAll), task(agentapi.ActionDoctorRun, nil))
	if ok.State != agentapi.TaskSucceeded || ok.Error != "" {
		t.Errorf("all fine: %+v", ok)
	}
}

func TestErrorsAndOutputAreRedactedBeforeTheyLeave(t *testing.T) {
	backend := &fakeBackend{startErr: errors.New("open s3:https://AKIA:secretkey@s3.example.com/b: access denied")}
	result, _ := run(New(backend, allowAll), task(agentapi.ActionBackupStart, map[string]string{"job": "db"}))
	if strings.Contains(result.Error, "secretkey") {
		t.Errorf("error = %q", result.Error)
	}
}

func TestATaskOfOneActionIsRateLimitedAndOthersAreNot(t *testing.T) {
	backend := &fakeBackend{}
	e := New(backend, allowAll)
	refusedCount := 0
	for range 10 {
		if result, _ := run(e, task(agentapi.ActionBackupStop, map[string]string{"job": "db"})); result.State == agentapi.TaskRefused {
			refusedCount++
			if !strings.Contains(result.Error, "too many") {
				t.Errorf("refusal = %q", result.Error)
			}
		}
	}
	if refusedCount != 7 || len(backend.calls) != 3 {
		t.Errorf("refused %d of 10, %d calls: want a burst of 3", refusedCount, len(backend.calls))
	}
	if result, _ := run(e, task(agentapi.ActionDoctorRun, nil)); result.State == agentapi.TaskRefused {
		t.Errorf("the limit of one action stopped another: %+v", result)
	}
}

func TestTheLogOfATaskIsCapped(t *testing.T) {
	var findings []doctor.Finding
	for i := range maxLogLines * 2 {
		findings = append(findings, doctor.Finding{Name: fmt.Sprint("check", i), Info: "ok"})
	}
	_, lines := run(New(&fakeBackend{findings: findings}, allowAll), task(agentapi.ActionDoctorRun, nil))
	if len(lines) != maxLogLines {
		t.Errorf("%d lines of log, want %d", len(lines), maxLogLines)
	}
}

func TestTheResultHasItsTimesAndIsCleaned(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	e := New(&fakeBackend{findings: []doctor.Finding{{Name: "x", Info: "a\x1b[31mb"}}}, allowAll)
	e.Now = func() time.Time { return now }
	result, _ := run(e, task(agentapi.ActionDoctorRun, nil))
	if !result.StartedAt.Equal(now) || !result.FinishedAt.Equal(now) || strings.ContainsRune(result.Output, 0x1b) {
		t.Errorf("result = %+v", result)
	}
}

func TestAPanicInATaskEndsItAsFailedInsteadOfTakingTheDaemonDown(t *testing.T) {
	backend := &fakeBackend{doctorPanics: true}
	result := loose(backend, config.Allow{Doctor: true}).Run(context.Background(), agentapi.Task{ID: 1, Action: agentapi.ActionDoctorRun}, func(string) {})
	if result.State != agentapi.TaskFailed || result.Error == "" {
		t.Errorf("result = %+v", result)
	}
}
