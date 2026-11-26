// Package restic drives the restic CLI against one repository.
package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/termtext"
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
	var exitErr *ExitError
	return errors.As(err, &exitErr) && exitErr.Code == exitLocked
}

// IsNoRepo reports whether err is restic finding no repository at the location.
func IsNoRepo(err error) bool {
	var exitErr *ExitError
	return errors.As(err, &exitErr) && exitErr.Code == exitNoRepository
}

// IsWrongPass reports whether err is restic rejecting the repository password.
func IsWrongPass(err error) bool {
	var exitErr *ExitError
	return errors.As(err, &exitErr) && exitErr.Code == exitWrongPass
}

// Client runs restic against a single repository.
type Client struct {
	Bin  string // default "restic"
	Repo config.Repository
	Host string
	// ExtraEnv is appended to the environment (e.g. PGPASSWORD for pg_dump,
	// which restic launches as a child process).
	ExtraEnv []string
	// LowPriority runs restic, and so the dump tool it starts, with a low CPU and disk priority
	// (nice 10, best-effort disk class 7) when `nice` and `ionice` are installed.
	LowPriority bool
	// UploadLimitKiB caps the upload speed in KiB/s (0 = no cap).
	UploadLimitKiB int
	// DropEnv names variables that restic and its child processes must not inherit: the ones the
	// config refers to with ${VAR}. Each of their values is already in the config, so whatever
	// restic or a dump tool needs reaches it through Repo and ExtraEnv, and the rest (a notifier
	// token, the password of another repository) is not theirs to see.
	DropEnv []string
}

// command is the process to start for restic and args: restic itself, or `nice`/`ionice`
// wrapping it. Both tools replace themselves with what they run, so the process stays the
// one that gets the interrupt on cancel, and what restic starts inherits the priority.
func (c *Client) command(args []string) (string, []string) {
	if c.UploadLimitKiB > 0 {
		args = append([]string{"--limit-upload", strconv.Itoa(c.UploadLimitKiB)}, args...)
	}
	argv := append([]string{c.bin()}, args...)
	if !c.LowPriority {
		return argv[0], argv[1:]
	}
	if ionice, err := exec.LookPath("ionice"); err == nil {
		argv = append([]string{ionice, "-c", "2", "-n", "7"}, argv...)
	}
	if nice, err := exec.LookPath("nice"); err == nil {
		argv = append([]string{nice, "-n", "10"}, argv...)
	}
	return argv[0], argv[1:]
}

func (c *Client) bin() string {
	if c.Bin == "" {
		return "restic"
	}
	return c.Bin
}

// InheritedEnv is the environment a tool started for a job may inherit: this process's own, without
// the variables in dropEnv (the ones the config refers to with ${VAR}, see Client.DropEnv), except the
// ordinary process settings that carry no secret.
func InheritedEnv(dropEnv []string) []string {
	var env []string
	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); !slices.Contains(dropEnv, name) || isProcessSetting(name) {
			env = append(env, entry)
		}
	}
	return env
}

// env passes the repository and secrets through the environment so they never
// appear in the process list.
func (c *Client) env() []string {
	env := InheritedEnv(c.DropEnv)
	env = append(env,
		"RESTIC_REPOSITORY="+c.Repo.URL,
		"RESTIC_PASSWORD="+c.Repo.Password,
	)
	for name, value := range c.Repo.Env {
		env = append(env, name+"="+value)
	}
	return append(env, c.ExtraEnv...)
}

// isProcessSetting reports ordinary settings of the process that carry no secret and that restic needs
// (PATH to find the dump tools, HOME for its cache, TMPDIR, the locale), so they stay even when the config uses one in a value such
// as ${HOME}/backups.
func isProcessSetting(name string) bool {
	switch name {
	case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TZ", "LANG", "LANGUAGE", "PWD", "HOSTNAME", "TERM":
		return true
	}
	return strings.HasPrefix(name, "LC_") || strings.HasPrefix(name, "XDG_")
}

// run executes restic and returns stdout. On failure the error carries
// restic's exit code and the message from its JSON error output or stderr.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	return c.runFiltered(ctx, nil, args...)
}

// runFiltered is run that also reads restic's progress while it works: each status line is
// handed to onStatus and not kept, so a backup of many hours does not pile its progress up in
// memory. Every other line (the summary, errors) is kept for the caller as usual. It is called
// from the goroutine that copies restic's output, one line at a time.
func (c *Client) runFiltered(ctx context.Context, onStatus func(Status), args ...string) ([]byte, error) {
	name, argv := c.command(args)
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.Env = c.env()
	if onStatus != nil {
		cmd.Env = append(cmd.Env, "RESTIC_PROGRESS_FPS=0.5") // a status line every two seconds is plenty
	}
	// On cancel, ask restic to stop cleanly so it can release its lock.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	stdout := &statusFilter{onStatus: onStatus}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctx.Err() != nil {
		// restic retries backend errors for a long time; its last log line
		// usually says why (e.g. "connection refused", "bucket does not exist").
		if reason := errorMessage(stdout.Bytes(), stderr.Bytes()); reason != "" {
			return stdout.Bytes(), fmt.Errorf("restic %s: %w (last output: %s)", args[0], ctx.Err(), reason)
		}
		return stdout.Bytes(), fmt.Errorf("restic %s: %w", args[0], ctx.Err())
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return stdout.Bytes(), fmt.Errorf("run restic: %w", err)
	}
	return stdout.Bytes(), &ExitError{Code: exitErr.ExitCode(), Message: errorMessage(stdout.Bytes(), stderr.Bytes())}
}

// Status is how far a backup has got, from restic's own progress lines. For data piped in
// (a database dump) restic does not know the total: TotalBytes is 0 and PercentDone is not
// meaningful.
type Status struct {
	BytesDone, TotalBytes int64
	FilesDone, TotalFiles int64
	PercentDone           float64 // 0 to 1, only when TotalBytes is known
}

// statusFilter is the writer restic's output goes to: it passes status lines to onStatus and
// keeps all the other lines.
type statusFilter struct {
	onStatus func(Status)
	kept     bytes.Buffer
	partial  []byte // the start of a line that has not ended yet
}

var statusLine = []byte(`"message_type":"status"`)

func (f *statusFilter) Write(chunk []byte) (int, error) {
	f.partial = append(f.partial, chunk...)
	for {
		end := bytes.IndexByte(f.partial, '\n')
		if end < 0 {
			break
		}
		f.line(f.partial[:end+1])
		f.partial = f.partial[end+1:]
	}
	if len(f.partial) > 1<<20 { // a "line" this long is not JSON progress: keep it as it is
		f.kept.Write(f.partial)
		f.partial = nil
	}
	return len(chunk), nil
}

func (f *statusFilter) line(line []byte) {
	if f.onStatus != nil && bytes.Contains(line, statusLine) {
		var msg message
		if json.Unmarshal(bytes.TrimSpace(line), &msg) == nil && msg.MessageType == "status" {
			f.onStatus(Status{
				BytesDone: msg.BytesDone, TotalBytes: msg.TotalBytes, FilesDone: msg.FilesDone, TotalFiles: msg.TotalFiles,
				PercentDone: msg.PercentDone,
			})
			return
		}
	}
	f.kept.Write(line)
}

// Bytes is everything that was not a status line, including a last line without its newline.
func (f *statusFilter) Bytes() []byte { return append(f.kept.Bytes(), f.partial...) }

// message is one JSON line of restic's --json output.
type message struct {
	MessageType string `json:"message_type"`
	Message     string `json:"message"`
	Error       struct {
		Message string `json:"message"`
	} `json:"error"`

	// summary fields
	FilesNew            int64  `json:"files_new"`
	FilesChanged        int64  `json:"files_changed"`
	DataAdded           int64  `json:"data_added"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
	SnapshotID          string `json:"snapshot_id"`

	// status fields
	PercentDone float64 `json:"percent_done"`
	TotalFiles  int64   `json:"total_files"`
	FilesDone   int64   `json:"files_done"`
	TotalBytes  int64   `json:"total_bytes"`
	BytesDone   int64   `json:"bytes_done"`
}

// errorMessage is the reason a restic command failed, with credentials masked and terminal escape
// sequences removed (file names in the error are chosen by other users): it is stored, logged, printed
// and sent in alerts.
func errorMessage(stdout, stderr []byte) string {
	return termtext.Clean(Redact(pickErrorMessage(stdout, stderr)))
}

// pickErrorMessage prefers restic's JSON exit_error, then per-item errors, then
// the last non-empty stderr line.
func pickErrorMessage(stdout, stderr []byte) string {
	var exitMsg string
	var itemErrs []string
	for _, msg := range parseMessages(append(append([]byte{}, stdout...), stderr...)) {
		switch msg.MessageType {
		case "exit_error":
			exitMsg = strings.TrimPrefix(msg.Message, "Fatal: ")
		case "error":
			itemErrs = append(itemErrs, msg.Error.Message)
		}
	}
	if exitMsg != "" {
		return exitMsg
	}
	if len(itemErrs) > 0 {
		return strings.Join(itemErrs, "; ")
	}
	errLines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	return strings.TrimSpace(errLines[len(errLines)-1])
}

func parseMessages(output []byte) []message {
	var msgs []message
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var msg message
		if json.Unmarshal(line, &msg) == nil && msg.MessageType != "" {
			msgs = append(msgs, msg)
		}
	}
	return msgs
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
	// OnStatus, when set, is called with restic's progress about every two seconds while the
	// backup runs (from another goroutine).
	OnStatus func(Status)
}

// ErrIncomplete means restic made a snapshot but could not read every file.
var ErrIncomplete = errors.New("snapshot is incomplete: some files could not be read")

func (c *Client) Backup(ctx context.Context, opts BackupOptions) (Summary, error) {
	args := []string{"backup", "--json", "--host", c.Host}
	for _, tag := range opts.Tags {
		args = append(args, "--tag", tag)
	}
	for _, pattern := range opts.Excludes {
		args = append(args, "--exclude", pattern)
	}
	if len(opts.Command) > 0 {
		args = append(args, "--stdin-filename", opts.StdinFilename, "--stdin-from-command", "--")
		args = append(args, opts.Command...)
	} else {
		args = append(args, "--")
		args = append(args, opts.Paths...)
	}

	stdout, err := c.runFiltered(ctx, opts.OnStatus, args...)
	var sum Summary
	for _, msg := range parseMessages(stdout) {
		if msg.MessageType == "summary" {
			sum = Summary{
				SnapshotID:   msg.SnapshotID,
				FilesNew:     msg.FilesNew,
				FilesChanged: msg.FilesChanged,
				BytesAdded:   msg.DataAdded,
				TotalBytes:   msg.TotalBytesProcessed,
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

// Forget applies the retention policy to snapshots with the given tags on this host. With prune it also
// removes the data no snapshot uses any more, which reads the whole repository and locks it for as long as
// it takes: the daemon leaves that to Prune, run once per repository on a schedule of its own.
func (c *Client) Forget(ctx context.Context, tags []string, retention config.Retention, prune bool) error {
	if retention.Empty() {
		return nil
	}
	args := []string{"forget", "--json", "--retry-lock", lockRetry, "--host", c.Host, "--group-by", "host,tags"}
	if prune {
		args = append(args, "--prune")
	}
	if len(tags) > 0 {
		args = append(args, "--tag", strings.Join(tags, ","))
	}
	for flag, keepN := range map[string]int{
		"--keep-last": retention.Last, "--keep-daily": retention.Daily, "--keep-weekly": retention.Weekly,
		"--keep-monthly": retention.Monthly, "--keep-yearly": retention.Yearly,
	} {
		if keepN > 0 {
			args = append(args, flag, strconv.Itoa(keepN))
		}
	}
	_, err := c.run(ctx, args...)
	return err
}

func (c *Client) Restore(ctx context.Context, snapshotID, targetDir string) error {
	_, err := c.run(ctx, "restore", "--json", snapshotID, "--target", targetDir)
	return err
}

// Init creates the repository (and the S3 bucket if needed). existed is true
// if it was already initialized.
func (c *Client) Init(ctx context.Context) (existed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	_, err = c.run(ctx, "init", "--json")
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
func (c *Client) Exists(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, existsTimeout)
	defer cancel()
	_, err := c.run(ctx, "cat", "config", "--no-lock")
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code == exitNoRepository {
		return false, nil
	}
	if err != nil && strings.Contains(err.Error(), "bucket does not exist") {
		return false, nil
	}
	return err == nil, err
}

// Prune removes the data of the repository that no snapshot uses any more (the snapshots `forget` dropped).
func (c *Client) Prune(ctx context.Context) error {
	_, err := c.run(ctx, "prune", "--retry-lock", lockRetry)
	return err
}

// lockRetry is how long `check`, `forget` and `prune` wait for a repository that another restic holds,
// instead of failing at once: the weekly check, the prune and the backups all run in the small hours.
const lockRetry = "30m"

// Verify checks repository integrity (structure only, no data read).
func (c *Client) Verify(ctx context.Context) error {
	_, err := c.run(ctx, "check", "--json", "--retry-lock", lockRetry)
	return err
}
