// Package backup runs backups by driving the restic CLI.
package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sentinel/internal/config"
)

// restic exit codes (restic >= 0.17).
const (
	exitIncomplete   = 3  // snapshot created, but some source files could not be read
	exitNoRepository = 10 // repository does not exist
	exitLocked       = 11 // failed to lock repository
	exitWrongPass    = 12
)

// ExitError is a non-zero restic exit with the most useful message we found.
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("restic exit %d: %s", e.Code, e.Message)
}

// IsLocked reports whether err is restic failing to lock the repository.
func IsLocked(err error) bool {
	var e *ExitError
	return errors.As(err, &e) && e.Code == exitLocked
}

// Restic runs restic against a single repository.
type Restic struct {
	Bin  string // default "restic"
	Repo config.Repository
	Host string
	// ExtraEnv is appended to the environment (e.g. PGPASSWORD for pg_dump,
	// which restic launches as a child process).
	ExtraEnv []string
}

func (r *Restic) bin() string {
	if r.Bin == "" {
		return "restic"
	}
	return r.Bin
}

// env passes the repository and secrets through the environment so they never
// appear in the process list.
func (r *Restic) env() []string {
	env := append(os.Environ(),
		"RESTIC_REPOSITORY="+r.Repo.URL,
		"RESTIC_PASSWORD="+r.Repo.Password,
	)
	for k, v := range r.Repo.Env {
		env = append(env, k+"="+v)
	}
	return append(env, r.ExtraEnv...)
}

// run executes restic and returns stdout. On failure the error carries
// restic's exit code and the message from its JSON error output or stderr.
func (r *Restic) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.bin(), args...)
	cmd.Env = r.env()
	// On cancel, ask restic to stop cleanly so it can release its lock.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctx.Err() != nil {
		// restic retries backend errors for a long time; its last log line
		// usually says why (e.g. "connection refused", "bucket does not exist").
		if msg := errorMessage(stdout.Bytes(), stderr.Bytes()); msg != "" {
			return stdout.Bytes(), fmt.Errorf("restic %s: %w (last output: %s)", args[0], ctx.Err(), msg)
		}
		return stdout.Bytes(), fmt.Errorf("restic %s: %w", args[0], ctx.Err())
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return stdout.Bytes(), fmt.Errorf("run restic: %w", err)
	}
	return stdout.Bytes(), &ExitError{Code: exitErr.ExitCode(), Message: errorMessage(stdout.Bytes(), stderr.Bytes())}
}

type jsonMessage struct {
	MessageType string `json:"message_type"`
	Message     string `json:"message"`
	Error       struct {
		Message string `json:"message"`
	} `json:"error"`
	Item string `json:"item"`

	// summary fields
	FilesNew            int64  `json:"files_new"`
	FilesChanged        int64  `json:"files_changed"`
	DataAdded           int64  `json:"data_added"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
	SnapshotID          string `json:"snapshot_id"`
}

// errorMessage prefers restic's JSON exit_error, then per-item errors, then
// the last non-empty stderr line.
func errorMessage(stdout, stderr []byte) string {
	var exitMsg string
	var itemErrs []string
	for _, line := range jsonLines(append(append([]byte{}, stdout...), stderr...)) {
		switch line.MessageType {
		case "exit_error":
			exitMsg = strings.TrimPrefix(line.Message, "Fatal: ")
		case "error":
			itemErrs = append(itemErrs, line.Error.Message)
		}
	}
	if exitMsg != "" {
		return exitMsg
	}
	if len(itemErrs) > 0 {
		return strings.Join(itemErrs, "; ")
	}
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func jsonLines(b []byte) []jsonMessage {
	var out []jsonMessage
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m jsonMessage
		if json.Unmarshal(line, &m) == nil && m.MessageType != "" {
			out = append(out, m)
		}
	}
	return out
}

type Summary struct {
	SnapshotID   string
	FilesNew     int64
	FilesChanged int64
	BytesAdded   int64
	TotalBytes   int64
}

type BackupOptions struct {
	Tags     []string
	Paths    []string
	Excludes []string
	// Command, when set, is run by restic and its stdout is stored as
	// StdinFilename. A non-zero exit fails the backup.
	Command       []string
	StdinFilename string
}

// ErrIncomplete means restic made a snapshot but could not read every file.
var ErrIncomplete = errors.New("snapshot is incomplete: some files could not be read")

func (r *Restic) Backup(ctx context.Context, o BackupOptions) (Summary, error) {
	args := []string{"backup", "--json", "--host", r.Host}
	for _, t := range o.Tags {
		args = append(args, "--tag", t)
	}
	for _, e := range o.Excludes {
		args = append(args, "--exclude", e)
	}
	if len(o.Command) > 0 {
		args = append(args, "--stdin-filename", o.StdinFilename, "--stdin-from-command", "--")
		args = append(args, o.Command...)
	} else {
		args = append(args, "--")
		args = append(args, o.Paths...)
	}

	out, err := r.run(ctx, args...)
	var sum Summary
	for _, m := range jsonLines(out) {
		if m.MessageType == "summary" {
			sum = Summary{
				SnapshotID:   m.SnapshotID,
				FilesNew:     m.FilesNew,
				FilesChanged: m.FilesChanged,
				BytesAdded:   m.DataAdded,
				TotalBytes:   m.TotalBytesProcessed,
			}
		}
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code == exitIncomplete {
		return sum, fmt.Errorf("%w (%s)", ErrIncomplete, exitErr.Message)
	}
	if err != nil {
		return sum, err
	}
	if sum.SnapshotID == "" {
		return sum, errors.New("restic reported success but no snapshot id")
	}
	return sum, nil
}

// Forget applies the retention policy to snapshots with the given tags on
// this host, then prunes unreferenced data.
func (r *Restic) Forget(ctx context.Context, tags []string, ret config.Retention) error {
	if ret.Empty() {
		return nil
	}
	args := []string{"forget", "--json", "--prune", "--host", r.Host, "--group-by", "host,tags"}
	if len(tags) > 0 {
		args = append(args, "--tag", strings.Join(tags, ","))
	}
	for flag, n := range map[string]int{
		"--keep-last": ret.Last, "--keep-daily": ret.Daily, "--keep-weekly": ret.Weekly,
		"--keep-monthly": ret.Monthly, "--keep-yearly": ret.Yearly,
	} {
		if n > 0 {
			args = append(args, flag, strconv.Itoa(n))
		}
	}
	_, err := r.run(ctx, args...)
	return err
}

type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
	Paths    []string  `json:"paths"`
	Summary  *struct {
		TotalBytes int64 `json:"total_bytes_processed"`
	} `json:"summary"`
}

func (r *Restic) Snapshots(ctx context.Context, tags []string) ([]Snapshot, error) {
	args := []string{"snapshots", "--json", "--host", r.Host}
	if len(tags) > 0 {
		args = append(args, "--tag", strings.Join(tags, ","))
	}
	out, err := r.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("parse snapshots: %w", err)
	}
	return snaps, nil
}

func (r *Restic) Restore(ctx context.Context, snapshotID, target string) error {
	_, err := r.run(ctx, "restore", "--json", snapshotID, "--target", target)
	return err
}

// Init creates the repository (and the S3 bucket if needed). existed is true
// if it was already initialized.
func (r *Restic) Init(ctx context.Context) (existed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	_, err = r.run(ctx, "init", "--json")
	var exitErr *ExitError
	// The wording differs by backend: "config file already exists" (local),
	// "repository master key and config already initialized" (s3).
	if errors.As(err, &exitErr) && (strings.Contains(exitErr.Message, "already exists") ||
		strings.Contains(exitErr.Message, "already initialized")) {
		return true, nil
	}
	return false, err
}

// restic retries backend errors for ~15 minutes; these bound the calls that
// should be quick so a misconfigured repository fails fast.
const (
	existsTimeout = 90 * time.Second
	initTimeout   = 5 * time.Minute
)

// Exists reports whether the repository is initialized and the password works.
func (r *Restic) Exists(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, existsTimeout)
	defer cancel()
	_, err := r.run(ctx, "cat", "config", "--no-lock")
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code == exitNoRepository {
		return false, nil
	}
	if err != nil && strings.Contains(err.Error(), "bucket does not exist") {
		return false, nil
	}
	return err == nil, err
}

// Check verifies repository integrity (structure only, no data read).
func (r *Restic) Check(ctx context.Context) error {
	_, err := r.run(ctx, "check", "--json")
	return err
}

var versionRe = regexp.MustCompile(`restic (\d+)\.(\d+)\.(\d+)`)

// Version returns restic's version as [major, minor, patch].
func Version(ctx context.Context, bin string) ([3]int, error) {
	var v [3]int
	if bin == "" {
		bin = "restic"
	}
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return v, err
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return v, fmt.Errorf("cannot parse restic version from %q", strings.TrimSpace(string(out)))
	}
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, nil
}
