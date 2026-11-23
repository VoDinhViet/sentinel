package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"sentinel/internal/backup"
	"sentinel/internal/config"
)

func shellScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSpawnDetachedRunsInASessionOfItsOwnAndLogsItsOutput(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "logs", "backup-x.log")
	tool := shellScript(t, `echo "out line"; echo "err line" >&2; sleep 0.3`)
	pid, exited, err := spawnDetached(tool, []string{"--flag", "value"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	if sid, ours := sessionOf(t, strconv.Itoa(pid)), sessionOf(t, "self"); sid != strconv.Itoa(pid) || sid == ours {
		t.Errorf("session of the child = %s (its pid %d), ours %s", sid, pid, ours)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the child did not finish")
	}
	log, _ := os.ReadFile(logPath)
	for _, want := range []string{"out line", "err line", "tool --flag value"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if info, _ := os.Stat(logPath); info.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v", info.Mode().Perm())
	}
}

func TestAwaitStartReportsAChildThatDiesAtOnceWithItsLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "logs", "backup-x.log")
	tool := shellScript(t, `echo "backup already running" >&2; exit 1`)
	pid, exited, err := spawnDetached(tool, nil, logPath)
	if err != nil {
		t.Fatal(err)
	}
	err = awaitStart(t.TempDir(), "x", pid, exited, logPath, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "backup already running") {
		t.Errorf("err = %v", err)
	}
}

func TestAwaitStartSucceedsWhenTheChildIsStillStartingAfterTheWait(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "logs", "backup-x.log")
	tool := shellScript(t, `sleep 5`)
	pid, exited, err := spawnDetached(tool, nil, logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if err := awaitStart(t.TempDir(), "x", pid, exited, logPath, 300*time.Millisecond); err != nil {
		t.Errorf("a slow start is not an error: %v", err)
	}
}

func TestStartDetachedBackupRefusesAJobThatIsAlreadyRunning(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{StorePath: filepath.Join(dir, "state.db")}
	progressDir := backup.ProgressDir(cfg.StorePath)
	if err := os.MkdirAll(progressDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"Job":"shop","PID":` + itoaPID() + `,"UpdatedAt":"` + time.Now().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(filepath.Join(progressDir, "shop.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := startDetachedBackup(cfg, "/x/config.yaml", "shop", ""); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("err = %v", err)
	}
}

func TestStopBackupAsksAndWaits(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{StorePath: filepath.Join(dir, "state.db")}
	if _, err := stopBackup(cfg, "shop", time.Second); err == nil || !strings.Contains(err.Error(), "no backup of") {
		t.Errorf("nothing runs: %v", err)
	}
	progressDir := backup.ProgressDir(cfg.StorePath)
	os.MkdirAll(progressDir, 0o700)
	progress := filepath.Join(progressDir, "shop.json")
	os.WriteFile(progress, []byte(`{"Job":"shop","PID":`+itoaPID()+`,"UpdatedAt":"`+time.Now().Format(time.RFC3339Nano)+`"}`), 0o600)
	// The "backup" ends a moment after it is asked to stop: the file goes away.
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(filepath.Join(progressDir, "shop.stop")); err == nil {
				os.Remove(progress)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	stopped, err := stopBackup(cfg, "shop", 5*time.Second)
	if err != nil || !stopped {
		t.Errorf("stopped = %v, err = %v", stopped, err)
	}
}

func itoaPID() string { return strconv.Itoa(os.Getpid()) }

// sessionOf is the session id of a process, from /proc/<pid>/stat ("pid (comm) state ppid pgrp session ...").
func sessionOf(t *testing.T, pid string) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(raw[strings.LastIndex(string(raw), ")")+1:]))
	return fields[3]
}
