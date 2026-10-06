package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"sentinel/internal/api"
	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/scheduler"
)

// CommandRunner long-polls the server for commands and executes them
// against the currently running daemon.
type CommandRunner struct {
	Client  *Client
	Current func() (*scheduler.Daemon, int64)
	Now     func() time.Time
}

func (c *CommandRunner) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *CommandRunner) Run(ctx context.Context) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		var cmds []api.Command
		err := c.Client.Do(ctx, http.MethodGet, "/api/agent/commands?wait=25s", nil, &cmds)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Debug("poll commands failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = 5 * time.Second
		for _, cmd := range cmds {
			go c.handle(ctx, cmd)
		}
	}
}

func (c *CommandRunner) handle(ctx context.Context, cmd api.Command) {
	slog.Info("running command from server", "id", cmd.ID, "type", cmd.Type)
	res := c.Execute(ctx, cmd)
	if !res.OK {
		slog.Warn("command failed", "id", cmd.ID, "type", cmd.Type, "error", res.Error)
	}
	// Report even if we are shutting down, so the UI does not wait forever.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := c.Client.Do(rctx, http.MethodPost, fmt.Sprintf("/api/agent/commands/%d/result", cmd.ID), res, nil); err != nil {
		slog.Error("report command result", "id", cmd.ID, "error", err)
	}
}

func fail(err error) api.CommandResult { return api.CommandResult{OK: false, Error: err.Error()} }

func ok(v any) api.CommandResult {
	out, _ := json.Marshal(v)
	return api.CommandResult{OK: true, Output: out}
}

// Execute runs one command and returns its result.
func (c *CommandRunner) Execute(ctx context.Context, cmd api.Command) api.CommandResult {
	d, _ := c.Current()
	if d == nil {
		return fail(errors.New("agent is still starting"))
	}
	switch cmd.Type {
	case api.CmdBackupRun:
		var a api.BackupRunArgs
		if err := json.Unmarshal(cmd.Args, &a); err != nil {
			return fail(err)
		}
		job, err := d.Config.FindBackup(a.Job)
		if err != nil {
			return fail(err)
		}
		runs := d.RunBackup(ctx, job)
		var parts, errs []string
		for _, r := range runs {
			if r.Success {
				parts = append(parts, fmt.Sprintf("%s: snapshot %.8s", r.Repository, r.SnapshotID))
			} else {
				errs = append(errs, r.Repository+": "+r.Error)
			}
		}
		res := ok(api.BackupRunOutput{Summary: strings.Join(parts, "; ")})
		if len(errs) > 0 {
			res.OK, res.Error = false, strings.Join(errs, "; ")
		}
		return res

	case api.CmdSnapshots:
		var a api.SnapshotsArgs
		if err := json.Unmarshal(cmd.Args, &a); err != nil {
			return fail(err)
		}
		job, rc, err := jobRepo(d, a.Job, a.Repository)
		if err != nil {
			return fail(err)
		}
		snaps, err := rc.Snapshots(ctx, backup.JobTags(job))
		if err != nil {
			return fail(err)
		}
		out := make([]api.Snapshot, 0, len(snaps))
		for _, s := range snaps {
			o := api.Snapshot{ID: s.ID, Time: s.Time, Paths: s.Paths}
			if s.Summary != nil {
				o.Size = s.Summary.TotalBytes
			}
			out = append(out, o)
		}
		return ok(out)

	case api.CmdRestore:
		var a api.RestoreArgs
		if err := json.Unmarshal(cmd.Args, &a); err != nil {
			return fail(err)
		}
		if !snapshotRe.MatchString(a.Snapshot) {
			return fail(fmt.Errorf("invalid snapshot id %q", a.Snapshot))
		}
		_, rc, err := jobRepo(d, a.Job, a.Repository)
		if err != nil {
			return fail(err)
		}
		target, err := RestoreTarget(d.Config.RestoreDir, a.Job, a.Snapshot, c.now())
		if err != nil {
			return fail(err)
		}
		if err := rc.Restore(ctx, a.Snapshot, target); err != nil {
			return fail(err)
		}
		return ok(api.RestoreOutput{Path: target})
	}
	return fail(fmt.Errorf("unknown command type %q", cmd.Type))
}

var (
	snapshotRe = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
	unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
)

func jobRepo(d *scheduler.Daemon, jobName, repo string) (*config.Backup, *backup.Restic, error) {
	job, err := d.Config.FindBackup(jobName)
	if err != nil {
		return nil, nil, err
	}
	if !slices.Contains(job.Repositories, repo) {
		return nil, nil, fmt.Errorf("job %q does not use repository %q", jobName, repo)
	}
	rc, err := d.Backups.Restic(repo)
	return job, rc, err
}

// RestoreTarget creates and returns a new directory under restoreDir. The
// server never chooses the path, so a restore can't overwrite live data.
func RestoreTarget(restoreDir, job, snapshot string, now time.Time) (string, error) {
	if restoreDir == "" {
		return "", errors.New("restore_dir is not set")
	}
	name := fmt.Sprintf("%s-%.8s-%s", unsafeName.ReplaceAllString(job, "_"), snapshot, now.Format("20060102-150405"))
	target := filepath.Join(restoreDir, name)
	if filepath.Dir(target) != filepath.Clean(restoreDir) {
		return "", fmt.Errorf("refusing restore target %q", target)
	}
	if err := os.MkdirAll(restoreDir, 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return "", fmt.Errorf("create restore directory: %w", err)
	}
	return target, nil
}
