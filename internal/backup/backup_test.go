package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/store"
)

func needRestic(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
}

func testRunner(t *testing.T, jobs ...config.Backup) (*Runner, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		ServerName: "test-host",
		Repositories: map[string]config.Repository{
			"local":  {URL: filepath.Join(dir, "repo1"), Password: "pw1"},
			"local2": {URL: filepath.Join(dir, "repo2"), Password: "pw2"},
		},
		Backups: jobs,
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
		rc, _ := r.Restic(n)
		existed, err := rc.Init(context.Background())
		if err != nil || existed {
			t.Fatalf("init %s: existed=%v err=%v", n, existed, err)
		}
		// Second init is a no-op.
		if existed, err := rc.Init(context.Background()); err != nil || !existed {
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

	job := config.Backup{
		Name: "files", Type: config.BackupFiles, Timeout: time.Minute,
		Paths: []string{src}, Exclude: []string{"*.tmp"},
		Repositories: []string{"local", "local2"},
		Retention:    config.Retention{Last: 2},
	}
	r, st := testRunner(t, job)
	initRepos(t, r, "local", "local2")

	for i := 0; i < 3; i++ {
		os.WriteFile(filepath.Join(src, "counter.txt"), []byte{byte('0' + i)}, 0o644)
		runs := r.Run(ctx, &r.Config.Backups[0])
		if len(runs) != 2 {
			t.Fatalf("want 2 runs, got %d", len(runs))
		}
		for _, run := range runs {
			if !run.Success || run.SnapshotID == "" || run.Error != "" {
				t.Fatalf("run %d failed: %+v", i, run)
			}
		}
	}

	rc, _ := r.Restic("local")
	snaps, err := rc.Snapshots(ctx, JobTags(&job))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("retention keep-last=2: got %d snapshots", len(snaps))
	}

	target := t.TempDir()
	latest := snaps[len(snaps)-1]
	if err := rc.Restore(ctx, latest.ID, target); err != nil {
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
	rc, _ := r.Restic("local")

	sum, err := rc.Backup(ctx, BackupOptions{
		Tags: []string{"job=db"}, StdinFilename: "app.dump",
		Command: []string{"sh", "-c", "echo dump-content"},
	})
	if err != nil || sum.SnapshotID == "" {
		t.Fatalf("backup: %+v, %v", sum, err)
	}

	// A failing dump command must fail the backup and leave no new snapshot.
	_, err = rc.Backup(ctx, BackupOptions{
		Tags: []string{"job=db"}, StdinFilename: "app.dump",
		Command: []string{"sh", "-c", "echo partial; exit 3"},
	})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("want failing dump to fail backup, got %v", err)
	}
	snaps, _ := rc.Snapshots(ctx, []string{"job=db"})
	if len(snaps) != 1 {
		t.Errorf("want 1 snapshot after failed dump, got %d", len(snaps))
	}
}

func TestBackupErrors(t *testing.T) {
	needRestic(t)
	ctx := context.Background()
	r, st := testRunner(t, config.Backup{
		Name: "gone", Type: config.BackupFiles, Timeout: time.Minute,
		Paths: []string{"/definitely/not/here"}, Repositories: []string{"local"},
	})
	rc, _ := r.Restic("local")

	// Repository not initialized.
	if ok, err := rc.Exists(ctx); ok || err != nil {
		t.Fatalf("Exists on missing repo = %v, %v", ok, err)
	}
	runs := r.Run(ctx, &r.Config.Backups[0])
	if runs[0].Success || runs[0].Error == "" {
		t.Fatalf("backup to missing repo should fail: %+v", runs[0])
	}

	initRepos(t, r, "local")
	runs = r.Run(ctx, &r.Config.Backups[0])
	if runs[0].Success || !strings.Contains(runs[0].Error, "do not exist") {
		t.Fatalf("missing source should fail with restic's message, got %+v", runs[0])
	}
	if _, ok, _ := st.LastSuccessfulBackup(ctx, "gone"); ok {
		t.Error("failed run recorded as success")
	}

	// Wrong password.
	bad := &Restic{Repo: config.Repository{URL: r.Config.Repositories["local"].URL, Password: "wrong"}, Host: "h"}
	_, err := bad.Snapshots(ctx, nil)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != exitWrongPass || !strings.Contains(err.Error(), "wrong password") {
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

func TestIncompleteSnapshotIsFailure(t *testing.T) {
	bin, _, _ := fakeRestic(t, `{"message_type":"error","error":{"message":"open /x: permission denied"},"item":"/x"}
{"message_type":"summary","files_new":3,"snapshot_id":"abc123"}`, 3)
	rc := &Restic{Bin: bin, Host: "h"}
	sum, err := rc.Backup(context.Background(), BackupOptions{Paths: []string{"/x"}})
	if !errors.Is(err, ErrIncomplete) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
	if sum.SnapshotID != "abc123" {
		t.Errorf("summary should still be parsed: %+v", sum)
	}
}

func TestLockedRepository(t *testing.T) {
	bin, _, _ := fakeRestic(t, `{"message_type":"exit_error","code":11,"message":"Fatal: unable to create lock"}`, 11)
	_, err := (&Restic{Bin: bin, Host: "h"}).Snapshots(context.Background(), nil)
	if !IsLocked(err) {
		t.Fatalf("IsLocked(%v) = false", err)
	}
}

func TestSecretsNotInArgs(t *testing.T) {
	bin, argsFile, envFile := fakeRestic(t, `{"message_type":"summary","snapshot_id":"abc"}`, 0)
	cfg := &config.Config{
		ServerName:   "h",
		Repositories: map[string]config.Repository{"r": {URL: "/repo", Password: "repo-secret"}},
	}
	job := config.Backup{
		Name: "db", Type: config.BackupPostgres, Timeout: time.Minute, Repositories: []string{"r"},
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

func TestVersion(t *testing.T) {
	bin, _, _ := fakeRestic(t, "restic 0.17.3 compiled with go1.23.1 on linux/amd64", 0)
	v, err := Version(context.Background(), bin)
	if err != nil || v != [3]int{0, 17, 3} {
		t.Fatalf("Version = %v, %v", v, err)
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
	job := config.Backup{Name: "f", Type: config.BackupFiles, Paths: []string{"/x"}, Timeout: time.Minute, Repositories: []string{"r"}}

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
