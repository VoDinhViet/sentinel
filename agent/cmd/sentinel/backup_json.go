package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/restic"
	"sentinel/agent/internal/store"
)

// snapListing is the JSON of `sentinel backup list -o json`. A repository that could not be
// read has Error set and no snapshots.
type snapListing struct {
	Job          string      `json:"job"`
	Repositories []repoSnaps `json:"repositories"`
}

type repoSnaps struct {
	Repository string        `json:"repository"`
	Snapshots  []snapOutcome `json:"snapshots"`
	Error      string        `json:"error,omitempty"`
}

type snapOutcome struct {
	ID         string    `json:"id"`
	ShortID    string    `json:"short_id"`
	Time       time.Time `json:"time"`
	TotalBytes *int64    `json:"total_bytes,omitempty"` // nil when restic did not report a size
}

func newRepoSnaps(repo string, snaps []restic.Snapshot, err error) repoSnaps {
	listing := repoSnaps{Repository: repo, Snapshots: make([]snapOutcome, 0, len(snaps))}
	if err != nil {
		listing.Error = err.Error()
		return listing
	}
	for _, snap := range snaps {
		outcome := snapOutcome{ID: snap.ID, ShortID: snap.ShortID, Time: snap.Time}
		if snap.Summary != nil {
			outcome.TotalBytes = &snap.Summary.TotalBytes
		}
		listing.Snapshots = append(listing.Snapshots, outcome)
	}
	return listing
}

func printSnapListing(w io.Writer, listing snapListing) {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "REPOSITORY\tSNAPSHOT\tTIME\tSIZE")
	for _, repo := range listing.Repositories {
		for _, snap := range repo.Snapshots {
			size := ""
			if snap.TotalBytes != nil {
				size = backup.HumanBytes(*snap.TotalBytes)
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", repo.Repository, snap.ShortID, snap.Time.Local().Format("2006-01-02 15:04"), size)
		}
	}
	table.Flush()
}

// backupRunReport is the JSON of `sentinel backup run -o json`.
type backupRunReport struct {
	Job  string       `json:"job"`
	Runs []runOutcome `json:"runs"`
}

type runOutcome struct {
	Repository      string `json:"repository"`
	Success         bool   `json:"success"`
	Result          string `json:"result"` // "ok", "ok (warning: ...)" or "FAILED: ..."
	SnapshotID      string `json:"snapshot_id,omitempty"`
	BytesAdded      int64  `json:"bytes_added"`
	DurationSeconds int64  `json:"duration_seconds"`
}

func newBackupRunReport(job string, runs []store.BackupRun) backupRunReport {
	report := backupRunReport{Job: job, Runs: make([]runOutcome, 0, len(runs))}
	for _, run := range runs {
		report.Runs = append(report.Runs, runOutcome{
			Repository:      run.Repository,
			Success:         run.Success,
			Result:          run.Summary(),
			SnapshotID:      run.SnapshotID,
			BytesAdded:      run.BytesAdded,
			DurationSeconds: int64(run.FinishedAt.Sub(run.StartedAt).Round(time.Second) / time.Second),
		})
	}
	return report
}

// detachedReport is the JSON of `sentinel backup run --detach -o json`.
type detachedReport struct {
	Job string `json:"job"`
	PID int    `json:"pid"`
	Log string `json:"log"`
}
