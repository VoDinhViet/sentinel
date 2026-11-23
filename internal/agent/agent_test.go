package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"sentinel/internal/alert"
	"sentinel/internal/api"
	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/scheduler"
	"sentinel/internal/server"
	"sentinel/internal/server/service"
	"sentinel/internal/store"
)

type env struct {
	svc    *service.Service
	ts     *httptest.Server
	client *Client
	agent  service.Agent
	store  *store.Store
	dir    string
	restic string
}

// newEnv starts a real server, registers an agent and opens its local store.
// restic is a fake script that succeeds and lists one snapshot.
func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	svc, err := service.Open(filepath.Join(dir, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	hash, _ := bcrypt.GenerateFromPassword([]byte("x"), bcrypt.MinCost)
	cfg := &config.ServerConfig{OfflineAfter: time.Minute}
	cfg.Admin.Username, cfg.Admin.PasswordHash = "admin", string(hash)
	ts := httptest.NewServer(server.New(cfg, svc))
	t.Cleanup(ts.Close)

	a, token, err := svc.CreateAgent(context.Background(), "web-01")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	restic := filepath.Join(dir, "restic")
	os.WriteFile(restic, []byte(`#!/bin/sh
case "$1" in
  snapshots) echo '[{"id":"abcdef1234567890","short_id":"abcdef12","time":"2026-10-01T02:00:00Z","paths":["/x"],"summary":{"total_bytes_processed":2048}}]' ;;
  restore) mkdir -p "$5" && echo restored > "$5/file.txt" ;;  # restore --json <id> --target <dir>
  *) echo '{"message_type":"summary","snapshot_id":"feedface12345678","data_added":5}' ;;
esac
`), 0o755)
	return &env{svc: svc, ts: ts, client: NewClient(ts.URL, token), agent: a, store: st, dir: dir, restic: restic}
}

func (e *env) daemon(cfg *config.Config) *scheduler.Daemon {
	return &scheduler.Daemon{
		Config:  cfg,
		Store:   e.store,
		Alerts:  &alert.Manager{Store: e.store},
		Backups: &backup.Runner{Config: cfg, Store: e.store, Bin: e.restic},
	}
}

func testConfig(dir string) *config.Config {
	return &config.Config{
		ServerName:   "web-01",
		RestoreDir:   filepath.Join(dir, "restore"),
		Repositories: map[string]config.Repository{"s3": {URL: "/r", Password: "p"}},
		Backups: []config.Backup{{Name: "site", Type: config.BackupFiles, Paths: []string{"/x"},
			Timeout: time.Minute, Repositories: []string{"s3"}}},
	}
}

func TestReporterSendsIncrementally(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	at := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		e.store.RecordBackup(ctx, store.BackupRun{Job: "site", Repository: "s3", StartedAt: at, FinishedAt: at, Success: true})
	}
	e.store.SaveAlert(ctx, store.Alert{Key: "disk:/", Firing: true, Since: at, LastNotified: at, Message: "disk full"})

	r := &Reporter{Client: e.client, Store: e.store, Info: func() api.Report {
		return api.Report{Hostname: "h", Jobs: []api.Job{{Name: "site", Repositories: []string{"s3"}}}}
	}}
	if err := r.Send(ctx); err != nil {
		t.Fatal(err)
	}
	if r.cursor.BackupRunID != 3 {
		t.Errorf("cursor after first send = %+v", r.cursor)
	}
	st, _ := e.svc.Status(ctx, e.agent.ID)
	if len(st.Backups) != 1 || len(st.Alerts) != 1 || st.Agent.Hostname != "h" {
		t.Errorf("server status = %+v", st)
	}

	e.store.RecordBackup(ctx, store.BackupRun{Job: "site", Repository: "s3", StartedAt: at, FinishedAt: at, Success: false, Error: "boom"})
	if err := r.Send(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ = e.svc.Status(ctx, e.agent.ID)
	if st.Backups[0].Error != "boom" || r.cursor.BackupRunID != 4 {
		t.Errorf("incremental send: %+v cursor %+v", st.Backups, r.cursor)
	}

	bad := &Reporter{Client: NewClient(e.ts.URL, "snt_wrong"), Store: e.store, Info: func() api.Report { return api.Report{} }}
	if err := bad.Send(ctx); !IsStatus(err, 401) {
		t.Errorf("wrong token err = %v", err)
	}
}

func TestCommandsEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newEnv(t)
	cfg := testConfig(e.dir)
	d := e.daemon(cfg)
	e.svc.Ingest(ctx, e.agent.ID, api.Report{Jobs: []api.Job{{Name: "site", Repositories: []string{"s3"}}}})

	runner := &CommandRunner{Client: e.client, Current: func() (*scheduler.Daemon, int64) { return d, 0 }}
	go runner.Run(ctx)

	wait := func(id int64) service.CommandRecord {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			rec, _ := e.svc.GetCommand(ctx, e.agent.ID, id)
			if rec.Done() {
				return rec
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("command %d did not finish", id)
		return service.CommandRecord{}
	}

	id, _ := e.svc.Enqueue(ctx, e.agent.ID, api.BackupRunArgs{Job: "site"})
	rec := wait(id)
	if rec.Status != api.StatusDone || !strings.Contains(string(rec.Output), "snapshot feedface") {
		t.Errorf("backup command = %+v (%s)", rec, rec.Output)
	}
	if _, ok, _ := e.store.LastSuccessfulBackup(ctx, "site"); !ok {
		t.Error("backup from UI not recorded locally")
	}

	id, _ = e.svc.Enqueue(ctx, e.agent.ID, api.SnapshotsArgs{Job: "site", Repository: "s3"})
	rec = wait(id)
	var snaps []api.Snapshot
	json.Unmarshal(rec.Output, &snaps)
	if rec.Status != api.StatusDone || len(snaps) != 1 || snaps[0].Size != 2048 {
		t.Errorf("snapshots command = %+v", rec)
	}

	id, _ = e.svc.Enqueue(ctx, e.agent.ID, api.RestoreArgs{Job: "site", Repository: "s3", Snapshot: "abcdef12"})
	rec = wait(id)
	var out api.RestoreOutput
	json.Unmarshal(rec.Output, &out)
	if rec.Status != api.StatusDone || filepath.Dir(out.Path) != cfg.RestoreDir {
		t.Fatalf("restore command = %+v (%s)", rec, rec.Output)
	}
	if b, _ := os.ReadFile(filepath.Join(out.Path, "file.txt")); string(b) != "restored\n" {
		t.Errorf("restored file = %q", b)
	}

	// A snapshot that is not one of the job's snapshots must not be restored.
	id, _ = e.svc.Enqueue(ctx, e.agent.ID, api.RestoreArgs{Job: "site", Repository: "s3", Snapshot: "deadbeef"})
	rec = wait(id)
	if rec.Status != api.StatusFailed || !strings.Contains(rec.Error, "does not belong") {
		t.Errorf("restore of foreign snapshot = %+v", rec)
	}
}

func TestRestoreTargetIsConfined(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	p, err := RestoreTarget(dir, "../../etc", "abcdef1234", now)
	if err != nil {
		t.Fatal(err)
	}
	// The job name is sanitised; the target must sit directly inside dir.
	if filepath.Dir(p) != dir || !strings.Contains(filepath.Base(p), "-abcdef12-20261006-120000") {
		t.Errorf("target = %s", p)
	}
	if _, err := RestoreTarget(dir, "../../etc", "abcdef1234", now); err == nil {
		t.Error("second restore at the same second should not reuse the directory")
	}
	if _, err := RestoreTarget("", "a", "abcdef12", now); err == nil {
		t.Error("empty restore dir accepted")
	}
}

const localManaged = `
server_name: web-01
central: { url: "http://unused", token: "t", managed: true }
repositories:
  s3: { url: /r, password: "${SENTINEL_TEST_PW}" }
backups:
  - { name: site, type: files, paths: [/x], repositories: [s3] }
`

func TestConfigSync(t *testing.T) {
	t.Setenv("SENTINEL_TEST_PW", "pw")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newEnv(t)

	var builds []*config.Config
	sup := NewSupervisor(func(cfg *config.Config) (*scheduler.Daemon, error) {
		builds = append(builds, cfg)
		return e.daemon(cfg), nil
	})
	sup.BusyPoll = 10 * time.Millisecond
	cs := &ConfigSync{Client: e.client, LocalRaw: []byte(localManaged),
		CachePath: filepath.Join(e.dir, "managed.json"), Supervisor: sup}

	// First start: the local config is uploaded as v1 (without local-only keys).
	cfg, v, err := cs.Initial(ctx)
	if err != nil || v != 1 || cfg.Central == nil || len(cfg.Backups) != 1 || cfg.Repositories["s3"].Password != "pw" {
		t.Fatalf("initial = %+v, v%d, %v", cfg, v, err)
	}
	doc, _, _ := e.svc.CurrentConfig(ctx, e.agent.ID)
	if strings.Contains(doc.YAML, "central") || !strings.Contains(doc.YAML, "${SENTINEL_TEST_PW}") {
		t.Errorf("uploaded config = %q", doc.YAML)
	}

	go sup.Run(ctx, cfg, v)
	go cs.Run(ctx, 20*time.Millisecond)
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// A version that cannot work here (missing secret) is rejected and reported.
	bad := doc.YAML + "notifiers:\n  telegram: { token: \"${SENTINEL_TEST_MISSING}\", chat_id: \"1\" }\n"
	v2, err := e.svc.SaveConfig(ctx, e.agent.ID, bad, "needs a secret the agent lacks")
	if err != nil {
		t.Fatal(err)
	}
	waitFor("rejection", func() bool {
		a, _ := e.svc.GetAgent(ctx, e.agent.ID)
		return a.ConfigStatus.Version == v2 && strings.Contains(a.ConfigStatus.Error, "SENTINEL_TEST_MISSING")
	})
	if _, cur := sup.Current(); cur != 1 {
		t.Errorf("agent switched to a bad config: v%d", cur)
	}

	// A good version is applied and reported.
	good := doc.YAML + "uptime:\n  - { name: web, type: http, url: \"https://example.com\" }\n"
	v3, _ := e.svc.SaveConfig(ctx, e.agent.ID, good, "add uptime")
	waitFor("reload", func() bool { _, cur := sup.Current(); return cur == v3 })
	waitFor("applied status", func() bool {
		a, _ := e.svc.GetAgent(ctx, e.agent.ID)
		return a.ConfigStatus.Version == v3 && a.ConfigStatus.Applied
	})
	d, _ := sup.Current()
	if len(d.Config.Uptime) != 1 || d.Config.ServerName != "web-01" {
		t.Errorf("reloaded config = %+v", d.Config)
	}

	// Offline restart uses the cached version.
	offline := &ConfigSync{Client: NewClient("http://127.0.0.1:1", "t"), LocalRaw: []byte(localManaged),
		CachePath: cs.CachePath, Supervisor: NewSupervisor(nil)}
	cfg, v, err = offline.Initial(ctx)
	if err != nil || v != v3 || len(cfg.Uptime) != 1 {
		t.Errorf("offline initial = v%d, %v", v, err)
	}
}

func TestReloadWaitsForRunningBackup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := newEnv(t)
	// A restic that takes a while, so the backup is still running at reload time.
	slow := filepath.Join(e.dir, "slow-restic")
	os.WriteFile(slow, []byte("#!/bin/sh\nsleep 1\necho '{\"message_type\":\"summary\",\"snapshot_id\":\"abc12345\"}'\n"), 0o755)

	cfg := testConfig(e.dir)
	var first *scheduler.Daemon
	sup := NewSupervisor(func(c *config.Config) (*scheduler.Daemon, error) {
		d := e.daemon(c)
		d.Backups.Bin = slow
		if first == nil {
			first = d
		}
		return d, nil
	})
	sup.BusyPoll = 20 * time.Millisecond
	go sup.Run(ctx, cfg, 1)
	for d, _ := sup.Current(); d == nil; d, _ = sup.Current() {
		time.Sleep(10 * time.Millisecond)
	}

	result := make(chan []store.BackupRun, 1)
	go func() { result <- first.RunBackup(ctx, &cfg.Backups[0]) }()
	time.Sleep(200 * time.Millisecond)
	sup.Reload(testConfig(e.dir), 2, nil)

	runs := <-result
	if !runs[0].Success {
		t.Fatalf("backup interrupted by reload: %+v", runs[0])
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, v := sup.Current(); v != 2; _, v = sup.Current() {
		if time.Now().After(deadline) {
			t.Fatal("reload never happened")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// executeOnAgent runs one command against a daemon that uses fakeRestic.
func executeOnAgent(t *testing.T, e *env, cfg *config.Config, fakeRestic string, command api.Command) api.CommandResult {
	t.Helper()
	d := e.daemon(cfg)
	d.Backups.Bin = fakeRestic
	runner := &CommandRunner{Client: e.client, Current: func() (*scheduler.Daemon, int64) { return d, 0 }}
	return runner.Execute(context.Background(), command)
}

func TestRestoreOnlyFromTheJobsOwnSnapshots(t *testing.T) {
	e := newEnv(t)
	cfg := testConfig(e.dir)
	restoreArgsFile := filepath.Join(e.dir, "restore-args")
	fakeRestic := filepath.Join(e.dir, "restic-two-jobs")
	// Like restic, list only the snapshots matching --tag: the other job's
	// snapshot shows up only when the caller does not filter by the job's tag.
	script := `#!/bin/sh
case "$1" in
  snapshots)
    case "$*" in
      *job=site*) echo '[{"id":"aaaa1111bbbb2222","tags":["job=site"]}]' ;;
      *) echo '[{"id":"aaaa1111bbbb2222","tags":["job=site"]},{"id":"cccc3333dddd4444","tags":["job=other"]}]' ;;
    esac ;;
  restore) echo "$*" > ` + restoreArgsFile + `; mkdir -p "$5" ;;
esac
`
	if err := os.WriteFile(fakeRestic, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	restoreCommand := func(snapshot string) api.Command {
		args, _ := json.Marshal(api.RestoreArgs{Job: "site", Repository: "s3", Snapshot: snapshot})
		return api.Command{ID: 1, Type: api.CmdRestore, Args: args}
	}

	foreign := executeOnAgent(t, e, cfg, fakeRestic, restoreCommand("cccc3333"))
	if foreign.OK || !strings.Contains(foreign.Error, "does not belong") {
		t.Errorf("restoring another job's snapshot = %+v", foreign)
	}
	if _, err := os.Stat(restoreArgsFile); err == nil {
		t.Error("restic restore ran for a snapshot of another job")
	}

	own := executeOnAgent(t, e, cfg, fakeRestic, restoreCommand("aaaa1111"))
	if !own.OK {
		t.Fatalf("restoring the job's own snapshot by prefix = %+v", own)
	}
	if args, _ := os.ReadFile(restoreArgsFile); !strings.Contains(string(args), "aaaa1111bbbb2222") {
		t.Errorf("restic must get the full snapshot id, got %q", args)
	}
}

func TestBackupNowReportsRetentionWarning(t *testing.T) {
	e := newEnv(t)
	cfg := testConfig(e.dir)
	cfg.Backups[0].Retention = config.Retention{Last: 1}
	fakeRestic := filepath.Join(e.dir, "restic-forget-fails")
	script := `#!/bin/sh
if [ "$1" = forget ]; then echo 'Fatal: repository is locked' >&2; exit 1; fi
echo '{"message_type":"summary","snapshot_id":"abcdef123456"}'
`
	if err := os.WriteFile(fakeRestic, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(api.BackupRunArgs{Job: "site"})
	result := executeOnAgent(t, e, cfg, fakeRestic, api.Command{ID: 1, Type: api.CmdBackupRun, Args: args})
	if !result.OK {
		t.Fatalf("a snapshot was made, so the command succeeds: %+v", result)
	}
	if !strings.Contains(string(result.Output), "snapshot abcdef12") || !strings.Contains(string(result.Output), "warning: retention failed") {
		t.Errorf("retention warning missing from the result: %s", result.Output)
	}
}
