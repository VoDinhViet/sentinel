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

func configuredJobRepositories(cfg *config.Config) []jobRepository {
	var jobRepositories []jobRepository
	for _, job := range cfg.Backups {
		for _, repository := range job.Repositories {
			jobRepositories = append(jobRepositories, jobRepository{job: job.Name, repository: repository})
		}
	}
	return jobRepositories
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

func (m manageModel) renderBusy() []string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	elapsed := time.Since(m.busyStart).Round(time.Second)
	return []string{
		lipglossAccent(frames[m.busyFrame%len(frames)]) + " " + m.busyText + dimStyle.Render("  "+elapsed.String()),
		"",
		m.keyBar("ctrl+c", "cancel"),
	}
}

func (m manageModel) renderSnapshots() []string {
	screenLines := []string{titleStyle.Render(fmt.Sprintf("Snapshots of %s in %s", m.snapshotJob, m.snapshotRepo)), ""}
	if len(m.snapshots) == 0 {
		return append(screenLines, dimStyle.Render("No snapshots for this job in this repository yet."), "", m.keyBar("esc", "back"))
	}
	visible := max(m.height-8, 3)
	first := 0
	if m.snapshotCursor >= visible {
		first = m.snapshotCursor - visible + 1
	}
	screenLines = append(screenLines, "  "+headerStyle.Render(fmt.Sprintf("%-10s %-17s %s", "SNAPSHOT", "TIME", "SIZE")))
	for i := first; i < min(first+visible, len(m.snapshots)); i++ {
		snapshot := m.snapshots[i]
		size := ""
		if snapshot.Summary != nil {
			size = humanBytes(snapshot.Summary.TotalBytes)
		}
		line := fmt.Sprintf("%-10s %-17s %s", shortID(snapshot.ID), snapshot.Time.Local().Format("2006-01-02 15:04"), size)
		if i == m.snapshotCursor {
			screenLines = append(screenLines, selectedStyle.Render("› "+line))
		} else {
			screenLines = append(screenLines, "  "+line)
		}
	}
	if hidden := len(m.snapshots) - (first + visible); hidden > 0 {
		screenLines = append(screenLines, dimStyle.Render(fmt.Sprintf("  … %d more", hidden)))
	}
	return append(screenLines, "", m.keyBar("↑↓", "select", "enter", "restore…", "esc", "back"))
}

func (m manageModel) renderTarget() []string {
	screenLines := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s of %s", shortID(m.chosen.ID), m.snapshotJob)),
		"",
		"Directory to restore into (absolute path):",
		"  " + m.target + lipglossAccent("▏"),
	}
	if m.inputError != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(m.inputError))
	}
	return append(screenLines, "", m.keyBar("enter", "continue", "esc", "back"))
}

func (m manageModel) renderConfirm() []string {
	screenLines := []string{
		titleStyle.Render(fmt.Sprintf("Restore %s of %s", shortID(m.chosen.ID), m.snapshotJob)),
		"",
		wrap(fmt.Sprintf("Snapshot %s (%s) from %s will be restored into %s.",
			shortID(m.chosen.ID), m.chosen.Time.Local().Format("2006-01-02 15:04"), m.snapshotRepo, m.target), m.width-2),
	}
	if m.target == "/" {
		screenLines = append(screenLines, "", severityStyle(sevWarn).Render("Target is / : files are restored in place and overwrite what is there."))
	}
	screenLines = append(screenLines, "", "Type the job name ("+m.snapshotJob+") to confirm:", "  "+m.confirm+lipglossAccent("▏"))
	if m.inputError != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(m.inputError))
	}
	return append(screenLines, "", m.keyBar("enter", "restore", "esc", "cancel"))
}

func (m manageModel) renderResult() []string {
	style := severityStyle(sevOK)
	if m.resultBad {
		style = severityStyle(sevBad)
	}
	first, rest, _ := strings.Cut(m.resultText, "\n")
	screenLines := []string{style.Render(wrap(first, m.width-2))}
	if rest != "" {
		screenLines = append(screenLines, rest)
	}
	return append(screenLines, "", m.keyBar("any key", "continue"))
}

func lipglossAccent(text string) string {
	return lipglossStyleAccent.Render(text)
}
