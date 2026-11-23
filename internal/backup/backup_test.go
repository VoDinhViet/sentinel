package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/restic"
	"sentinel/internal/store"
)

func needRestic(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
}

func testRunner(t *testing.T, jobs ...config.Job) (*Runner, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		ServerName: "test-host",
		Repositories: map[string]config.Repository{
			"local":  {URL: filepath.Join(dir, "repo1"), Password: "pw1"},
			"local2": {URL: filepath.Join(dir, "repo2"), Password: "pw2"},
		},
		Jobs: jobs,
	}
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Runner{Config: cfg, Store: st}, st
}

func initRepos(t *testing.T, r *Runner, names ...string) {
	t.Helper()
	for _, n := range names {
		client, _ := r.Restic(n)
		existed, err := client.Init(context.Background())
		if err != nil || existed {
			t.Fatalf("init %s: existed=%v err=%v", n, existed, err)
		}
		// Second init is a no-op.
		if existed, err := client.Init(context.Background()); err != nil || !existed {
			t.Fatalf("re-init %s: existed=%v err=%v", n, existed, err)
		}
	}
}

func TestFilesBackupRestoreRetention(t *testing.T) {
	needRestic(t)
	ctx := context.Background()
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "keep.txt"), []byte("important"), 0o644)
	os.WriteFile(filepath.Join(src, "skip.tmp"), []byte("junk"), 0o644)

	job := config.Job{
		Name: "files", Type: config.JobFiles, Timeout: time.Minute,
		Paths: []string{src}, Excludes: []string{"*.tmp"},
		Repositories: []string{"local", "local2"},
		Retention:    config.Retention{Last: 2},
	}
	r, st := testRunner(t, job)
	initRepos(t, r, "local", "local2")

	for i := 0; i < 3; i++ {
		os.WriteFile(filepath.Join(src, "counter.txt"), []byte{byte('0' + i)}, 0o644)
		runs := r.Run(ctx, &r.Config.Jobs[0])
		if len(runs) != 2 {
			t.Fatalf("want 2 runs, got %d", len(runs))
		}
		for _, run := range runs {
			if !run.Success || run.SnapshotID == "" || run.Error != "" {
				t.Fatalf("run %d failed: %+v", i, run)
			}
		}
	}

	client, _ := r.Restic("local")
	snaps, err := client.Snapshots(ctx, JobTags(&job))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("retention keep-last=2: got %d snapshots", len(snaps))
	}

	target := t.TempDir()
	latest := snaps[len(snaps)-1]
	if err := client.Restore(ctx, latest.ID, target); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(target, src)
	if b, err := os.ReadFile(filepath.Join(restored, "keep.txt")); err != nil || string(b) != "important" {
		t.Errorf("keep.txt = %q, %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(restored, "counter.txt")); string(b) != "2" {
		t.Errorf("restored counter = %q, want latest (2)", b)
	}
	if _, err := os.Stat(filepath.Join(restored, "skip.tmp")); !os.IsNotExist(err) {
		t.Errorf("excluded file was backed up")
	}

	if _, ok, _ := st.LastSuccessfulBackup(ctx, "files"); !ok {
		t.Error("success not recorded in store")
	}
}

func TestStdinCommandBackup(t *testing.T) {
	needRestic(t)
	ctx := context.Background()
	r, _ := testRunner(t)
	initRepos(t, r, "local")
	client, _ := r.Restic("local")

	sum, err := client.Backup(ctx, restic.BackupOptions{
		Tags: []string{"job=db"}, StdinFilename: "app.dump",
		Command: []string{"sh", "-c", "echo dump-content"},
	})
	if err != nil || sum.SnapshotID == "" {
		t.Fatalf("backup: %+v, %v", sum, err)
	}

	// A failing dump command must fail the backup and leave no new snapshot.
	_, err = client.Backup(ctx, restic.BackupOptions{
		Tags: []string{"job=db"}, StdinFilename: "app.dump",
		Command: []string{"sh", "-c", "echo partial; exit 3"},
	})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("want failing dump to fail backup, got %v", err)
	}
	snaps, _ := client.Snapshots(ctx, []string{"job=db"})
	if len(snaps) != 1 {
		t.Errorf("want 1 snapshot after failed dump, got %d", len(snaps))
	}
}

func TestBackupErrors(t *testing.T) {
	needRestic(t)
	ctx := context.Background()
	r, st := testRunner(t, config.Job{
		Name: "gone", Type: config.JobFiles, Timeout: time.Minute,
		Paths: []string{"/definitely/not/here"}, Repositories: []string{"local"},
	})
	client, _ := r.Restic("local")

	// Repository not initialized.
	if ok, err := client.Exists(ctx); ok || err != nil {
		t.Fatalf("Exists on missing repo = %v, %v", ok, err)
	}
	runs := r.Run(ctx, &r.Config.Jobs[0])
	if runs[0].Success || runs[0].Error == "" {
		t.Fatalf("backup to missing repo should fail: %+v", runs[0])
	}

	initRepos(t, r, "local")
	runs = r.Run(ctx, &r.Config.Jobs[0])
	if runs[0].Success || !strings.Contains(runs[0].Error, "do not exist") {
		t.Fatalf("missing source should fail with restic's message, got %+v", runs[0])
	}
	if _, ok, _ := st.LastSuccessfulBackup(ctx, "gone"); ok {
		t.Error("failed run recorded as success")
	}

	// Wrong password.
	bad := &restic.Client{Repo: config.Repository{URL: r.Config.Repositories["local"].URL, Password: "wrong"}, Host: "h"}
	_, err := bad.Snapshots(ctx, nil)
	if !restic.IsWrongPass(err) || !strings.Contains(err.Error(), "wrong password") {
		t.Fatalf("wrong password error = %v", err)
	}
}

// fakeRestic writes a script that prints out and exits with code, recording
// its arguments and RESTIC_PASSWORD to files in dir.
func fakeRestic(t *testing.T, out string, code int) (bin, argsFile, envFile string) {
	t.Helper()
	dir := t.TempDir()
	bin, argsFile, envFile = filepath.Join(dir, "restic"), filepath.Join(dir, "args"), filepath.Join(dir, "env")
	script := "#!/bin/sh\n" +
		"echo \"$@\" > " + argsFile + "\n" +
		"echo \"$RESTIC_PASSWORD $PGPASSWORD\" > " + envFile + "\n" +
		"cat <<'EOF'\n" + out + "\nEOF\n" +
		"exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile, envFile
}

func TestSecretsNotInArgs(t *testing.T) {
	bin, argsFile, envFile := fakeRestic(t, `{"message_type":"summary","snapshot_id":"abc"}`, 0)
	cfg := &config.Config{
		ServerName:   "h",
		Repositories: map[string]config.Repository{"r": {URL: "/repo", Password: "repo-secret"}},
	}
	job := config.Job{
		Name: "db", Type: config.JobPostgres, Timeout: time.Minute, Repositories: []string{"r"},
		Postgres: &config.Postgres{Host: "db.local", Port: 5433, User: "app", Password: "pg-secret", Database: "shop"},
	}
	runs := (&Runner{Config: cfg, Bin: bin}).Run(context.Background(), &job)
	if !runs[0].Success {
		t.Fatalf("run failed: %+v", runs[0])
	}
	args, _ := os.ReadFile(argsFile)
	env, _ := os.ReadFile(envFile)
	if strings.Contains(string(args), "secret") {
		t.Errorf("secret leaked into argv: %s", args)
	}
	if strings.TrimSpace(string(env)) != "repo-secret pg-secret" {
		t.Errorf("secrets not passed via env: %q", env)
	}
	for _, want := range []string{"--stdin-filename shop.dump", "--stdin-from-command -- pg_dump", "--port 5433", "--dbname shop", "--tag job=db", "--host h"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args missing %q: %s", want, args)
		}
	}
}

func TestLockRetry(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	// Locked on the first call, succeeds on the second.
	script := "#!/bin/sh\n" +
		"if [ ! -f " + dir + "/called ]; then touch " + dir + "/called\n" +
		"  echo '{\"message_type\":\"exit_error\",\"code\":11,\"message\":\"locked\"}'; exit 11; fi\n" +
		"echo '{\"message_type\":\"summary\",\"snapshot_id\":\"abc\"}'\n"
	os.WriteFile(bin, []byte(script), 0o755)

	cfg := &config.Config{ServerName: "h", Repositories: map[string]config.Repository{"r": {URL: "/r", Password: "p"}}}
	job := config.Job{Name: "f", Type: config.JobFiles, Paths: []string{"/x"}, Timeout: time.Minute, Repositories: []string{"r"}}

	noRetry := (&Runner{Config: cfg, Bin: bin}).Run(context.Background(), &job)
	if noRetry[0].Success || !strings.Contains(noRetry[0].Error, "locked") {
		t.Fatalf("without retries a lock should fail: %+v", noRetry[0])
	}
	os.Remove(dir + "/called")
	runs := (&Runner{Config: cfg, Bin: bin, LockRetries: 1, LockRetryDelay: time.Millisecond}).Run(context.Background(), &job)
	if !runs[0].Success {
		t.Fatalf("retry should succeed: %+v", runs[0])
	}
}

func TestRetentionFailureKeepsBackupSuccessful(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "restic")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = forget ]; then echo 'Fatal: repository is locked' >&2; exit 1; fi\n" +
		"echo '{\"message_type\":\"summary\",\"snapshot_id\":\"abcdef123456\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ServerName:   "h",
		Repositories: map[string]config.Repository{"r": {URL: "/repo", Password: "p"}},
	}
	job := config.Job{
		Name: "site", Type: config.JobFiles, Paths: []string{"/x"}, Timeout: time.Minute,
		Repositories: []string{"r"}, Retention: config.Retention{Last: 3},
	}
	runs := (&Runner{Config: cfg, Bin: bin}).Run(context.Background(), &job)
	if !runs[0].Success || runs[0].SnapshotID != "abcdef123456" {
		t.Fatalf("a snapshot was made, so the run must count as successful: %+v", runs[0])
	}
	if !strings.Contains(runs[0].Error, "retention failed") {
		t.Errorf("retention failure not reported: %q", runs[0].Error)
	}
}

func TestJobLockIsSharedAcrossRunners(t *testing.T) {
	slowRestic := filepath.Join(t.TempDir(), "restic")
	script := "#!/bin/sh\nsleep 1\necho '{\"message_type\":\"summary\",\"snapshot_id\":\"abcdef123456\"}'\n"
	if err := os.WriteFile(slowRestic, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ServerName:   "h",
		StorePath:    filepath.Join(t.TempDir(), "state.db"),
		Repositories: map[string]config.Repository{"r": {URL: "/repo", Password: "p"}},
	}
	job := config.Job{Name: "site/with slash", Type: config.JobFiles, Paths: []string{"/x"}, Timeout: time.Minute, Repositories: []string{"r"}}

	// Two runners stand for the daemon and a `sentinel backup run` process.
	first := make(chan []store.BackupRun, 1)
	go func() { first <- (&Runner{Config: cfg, Bin: slowRestic}).Run(context.Background(), &job) }()
	time.Sleep(300 * time.Millisecond)

	overlapping := (&Runner{Config: cfg, Bin: slowRestic}).Run(context.Background(), &job)
	if len(overlapping) != 1 || overlapping[0].Error != AlreadyRunning || overlapping[0].Success {
		t.Fatalf("overlapping run = %+v", overlapping)
	}
	if runs := <-first; !runs[0].Success {
		t.Fatalf("first run = %+v", runs[0])
	}
	if runs := (&Runner{Config: cfg, Bin: slowRestic}).Run(context.Background(), &job); !runs[0].Success {
		t.Fatalf("run after the first finished = %+v", runs[0])
	}
}
