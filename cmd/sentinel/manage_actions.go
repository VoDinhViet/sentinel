package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/configedit"
	"sentinel/internal/engine"
	"sentinel/internal/store"
)

// jobRepository is one backup the manager can act on: a job and one of its repositories.
type jobRepository struct {
	job, repository string
}

func configuredPairs(cfg *config.Config) []jobRepository {
	var pairs []jobRepository
	for _, job := range cfg.Backups {
		for _, repository := range job.Repositories {
			pairs = append(pairs, jobRepository{job: job.Name, repository: repository})
		}
	}
	return pairs
}

// manageActions are the things the manager does besides reading the store. They
// are fields so tests can replace them.
type manageActions struct {
	runBackup func(ctx context.Context, job, repository string) ([]store.BackupRun, error)
	snapshots func(ctx context.Context, job, repository string) ([]backup.Snapshot, error)
	// restore returns the command that loads a restored database dump, or "".
	restore func(ctx context.Context, job, repository string, snapshot backup.Snapshot, target string) (string, error)
	// The config file: read the jobs, save a database job, remove one.
	listJobs  func() (configedit.Listing, error)
	saveJob   func(original string, job configedit.Job) error
	deleteJob func(name string) error
}

func newManageActions(cfg *config.Config, st *store.Store, configPath string) manageActions {
	jobAndRestic := func(jobName, repository string) (*config.Backup, *backup.Restic, error) {
		job, err := cfg.FindBackup(jobName)
		if err != nil {
			return nil, nil, err
		}
		restic, err := (&backup.Runner{Config: cfg}).Restic(repository)
		return job, restic, err
	}
	return manageActions{
		listJobs:  func() (configedit.Listing, error) { return configedit.List(configPath) },
		saveJob:   func(original string, job configedit.Job) error { return configedit.Save(configPath, original, job) },
		deleteJob: func(name string) error { return configedit.Delete(configPath, name) },
		runBackup: func(ctx context.Context, jobName, repository string) ([]store.BackupRun, error) {
			job, err := cfg.FindBackup(jobName)
			if err != nil {
				return nil, err
			}
			jobCopy := *job
			jobCopy.Repositories = []string{repository}
			// The runner's job lock keeps this from overlapping the daemon or a CLI run.
			return (&backup.Runner{Config: cfg, Store: st}).Run(ctx, &jobCopy), nil
		},
		snapshots: func(ctx context.Context, jobName, repository string) ([]backup.Snapshot, error) {
			job, restic, err := jobAndRestic(jobName, repository)
			if err != nil {
				return nil, err
			}
			snapshots, err := restic.Snapshots(ctx, backup.JobTags(job))
			if err != nil {
				return nil, err
			}
			for i, j := 0, len(snapshots)-1; i < j; i, j = i+1, j-1 { // newest first
				snapshots[i], snapshots[j] = snapshots[j], snapshots[i]
			}
			return snapshots, nil
		},
		restore: func(ctx context.Context, jobName, repository string, snapshot backup.Snapshot, target string) (string, error) {
			job, restic, err := jobAndRestic(jobName, repository)
			if err != nil {
				return "", err
			}
			// Look the snapshot up among the job's own, as `backup restore` does.
			own, err := restic.JobSnapshot(ctx, backup.JobTags(job), snapshot.ID)
			if errors.Is(err, backup.ErrForeignSnapshot) {
				return "", fmt.Errorf("snapshot %s is not a snapshot of job %q in %s", shortID(snapshot.ID), jobName, repository)
			}
			if err != nil {
				return "", err
			}
			if err := restic.Restore(ctx, own.ID, target); err != nil {
				return "", err
			}
			if dbEngine, ok := engine.For(job.Type); ok {
				return dbEngine.RestoreHint(job, target), nil
			}
			return "", nil
		},
	}
}

// renderBusy, renderSnapshots, renderTarget, renderConfirm and renderResult are the
// screens other than the three tabs.

func (d manager) renderBusy() []string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	elapsed := time.Since(d.busyStart).Round(time.Second)
	return []string{
		lipglossAccent(frames[d.busyFrame%len(frames)]) + " " + d.busyText + dimStyle.Render("  "+elapsed.String()),
		"",
		dimStyle.Render("ctrl+c cancel"),
	}
}

func (d manager) renderSnapshots() []string {
	out := []string{titleStyle.Render(fmt.Sprintf("Snapshots of %s in %s", d.snapshotJob, d.snapshotRepo)), ""}
	if len(d.snapshots) == 0 {
		return append(out, dimStyle.Render("No snapshots for this job in this repository yet."), "", dimStyle.Render("esc back"))
	}
	visible := max(d.height-8, 3)
	first := 0
	if d.snapshotCursor >= visible {
		first = d.snapshotCursor - visible + 1
	}
	out = append(out, "  "+headerStyle.Render(fmt.Sprintf("%-10s %-17s %s", "SNAPSHOT", "TIME", "SIZE")))
	for i := first; i < min(first+visible, len(d.snapshots)); i++ {
		snapshot := d.snapshots[i]
		size := ""
		if snapshot.Summary != nil {
			size = humanBytes(snapshot.Summary.TotalBytes)
		}
		line := fmt.Sprintf("%-10s %-17s %s", shortID(snapshot.ID), snapshot.Time.Local().Format("2006-01-02 15:04"), size)
		if i == d.snapshotCursor {
			out = append(out, selectedStyle.Render("› "+line))
		} else {
			out = append(out, "  "+line)
		}
	}
	if hidden := len(d.snapshots) - (first + visible); hidden > 0 {
		out = append(out, dimStyle.Render(fmt.Sprintf("  … %d more", hidden)))
	}
	return append(out, "", dimStyle.Render("↑↓ select · enter restore… · esc back"))
}

func (d manager) renderTarget() []string {
	out := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s of %s", shortID(d.chosen.ID), d.snapshotJob)),
		"",
		"Directory to restore into (absolute path):",
		"  " + d.target + lipglossAccent("▏"),
	}
	if d.inputError != "" {
		out = append(out, "", severityStyle(sevBad).Render(d.inputError))
	}
	return append(out, "", dimStyle.Render("enter continue · esc back"))
}

func (d manager) renderConfirm() []string {
	out := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s of %s", shortID(d.chosen.ID), d.snapshotJob)),
		"",
		wrap(fmt.Sprintf("Snapshot %s (%s) from %s will be restored into %s.",
			shortID(d.chosen.ID), d.chosen.Time.Local().Format("2006-01-02 15:04"), d.snapshotRepo, d.target), d.width-2),
	}
	if d.target == "/" {
		out = append(out, "", severityStyle(sevWarn).Render("Target is / : files are restored in place and overwrite what is there."))
	}
	out = append(out, "", "Type the job name ("+d.snapshotJob+") to confirm:", "  "+d.confirm+lipglossAccent("▏"))
	if d.inputError != "" {
		out = append(out, "", severityStyle(sevBad).Render(d.inputError))
	}
	return append(out, "", dimStyle.Render("enter restore · esc cancel"))
}

func (d manager) renderResult() []string {
	style := severityStyle(sevOK)
	if d.resultBad {
		style = severityStyle(sevBad)
	}
	first, rest, _ := strings.Cut(d.resultText, "\n")
	out := []string{style.Render(wrap(first, d.width-2))}
	if rest != "" {
		out = append(out, rest)
	}
	return append(out, "", dimStyle.Render("any key to continue"))
}

func lipglossAccent(text string) string {
	return lipglossStyleAccent.Render(text)
}
