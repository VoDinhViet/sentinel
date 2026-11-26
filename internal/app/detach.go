package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sentinel/internal/backup"
	"sentinel/internal/config"
)

// logRotateSize is how big a detached backup's log may get before the next run starts a new one.
const logRotateSize = 1 << 20

// startWait is how long StartBackup waits for the new process to show it is running.
const startWait = 3 * time.Second

// StartBackup runs a backup of the job in a process of its own, so it goes on when the terminal
// is closed, the SSH connection drops or the manager is left. repo is one of the job's
// repositories, or "" for all of them. It returns once the backup has got going, or has failed
// to start (and says why). The job lock keeps it from overlapping any other run of the job.
func (s *Service) StartBackup(jobName, repo string) (pid int, logPath string, err error) {
	job, err := s.cfg.FindJob(jobName)
	if err != nil {
		return 0, "", err
	}
	if repo != "" {
		if _, err := job.OnlyRepo(repo); err != nil {
			return 0, "", err
		}
	}
	return startDetachedBackup(s.cfg, s.configPath, job.Name, repo)
}

// RequestStop asks the running backup of a job to stop, and does not wait for it to go.
func (s *Service) RequestStop(jobName string) error {
	return backup.RequestStop(backup.ProgressDir(s.cfg.StorePath), jobName, time.Now())
}

// StopBackup asks the running backup of a job to stop and waits up to wait for it to go.
// stopped is false when it is still shutting down after that.
func (s *Service) StopBackup(jobName string, wait time.Duration) (stopped bool, err error) {
	return stopBackup(s.cfg, jobName, wait)
}

// detachedLogPath is the log of the detached backups of a job, next to the state database.
func detachedLogPath(cfg *config.Config, jobName string) string {
	name := strings.NewReplacer("/", "_", "\\", "_", "\x00", "_").Replace(jobName)
	return filepath.Join(filepath.Dir(cfg.StorePath), "logs", "backup-"+name+".log")
}

func startDetachedBackup(cfg *config.Config, configPath, jobName, repo string) (pid int, logPath string, err error) {
	progressDir := backup.ProgressDir(cfg.StorePath)
	for _, running := range backup.ReadRunning(progressDir, time.Now()) {
		if running.Job == jobName {
			return 0, "", fmt.Errorf("a backup of %s is already running (pid %d)", jobName, running.PID)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, "", fmt.Errorf("find the sentinel binary: %w", err)
	}
	args := []string{"backup", "run", jobName, "--config", configPath}
	if repo != "" {
		args = append(args, "--repository", repo)
	}
	logPath = detachedLogPath(cfg, jobName)
	pid, exited, err := spawnDetached(exe, args, logPath)
	if err != nil {
		return 0, logPath, err
	}
	return pid, logPath, awaitStart(progressDir, jobName, pid, exited, logPath, startWait)
}

// spawnDetached starts a command in a new session with its output appended to logPath (0600),
// and returns its pid and a channel that gets the command's exit (it also reaps the process).
// The command gets the environment of this process, so the variables with passwords are there
// and nothing secret is put in its arguments.
func spawnDetached(bin string, args []string, logPath string) (int, <-chan error, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, nil, fmt.Errorf("create the log directory: %w", err)
	}
	if info, err := os.Stat(logPath); err == nil && info.Size() > logRotateSize {
		_ = os.Rename(logPath, logPath+".1")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, nil, fmt.Errorf("open the log: %w", err)
	}
	defer logFile.Close() // the child has its own copy
	fmt.Fprintf(logFile, "--- %s: %s %s\n", time.Now().Format(time.RFC3339), filepath.Base(bin), strings.Join(args, " "))
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, nil, fmt.Errorf("start %s: %w", filepath.Base(bin), err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return cmd.Process.Pid, exited, nil
}

// awaitStart waits until the detached process is seen running (its progress file is there),
// has exited (an error, with the last lines of its log), or the wait is over (it is still
// starting, which is not an error).
func awaitStart(progressDir, jobName string, pid int, exited <-chan error, logPath string, wait time.Duration) error {
	deadline := time.After(wait)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case exitErr := <-exited:
			if exitErr == nil {
				return fmt.Errorf("the backup stopped right away: %s", lastLogLines(logPath, 3))
			}
			return fmt.Errorf("the backup stopped right away: %s: %w", lastLogLines(logPath, 3), exitErr)
		case <-deadline:
			return nil
		case <-tick.C:
			for _, running := range backup.ReadRunning(progressDir, time.Now()) {
				if running.Job == jobName && running.PID == pid {
					return nil
				}
			}
		}
	}
}

// lastLogLines is the end of a log file, for an error message.
func lastLogLines(path string, count int) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "no log"
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "---") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "no output (see " + path + ")"
	}
	return strings.Join(lines[max(len(lines)-count, 0):], "; ")
}

func stopBackup(cfg *config.Config, jobName string, wait time.Duration) (stopped bool, err error) {
	progressDir := backup.ProgressDir(cfg.StorePath)
	if err := backup.RequestStop(progressDir, jobName, time.Now()); err != nil {
		return false, err
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		stillRunning := false
		for _, running := range backup.ReadRunning(progressDir, time.Now()) {
			stillRunning = stillRunning || running.Job == jobName
		}
		if !stillRunning {
			return true, nil
		}
	}
	return false, nil
}
