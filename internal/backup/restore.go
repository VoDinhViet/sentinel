package backup

import (
	"context"
	"fmt"

	"sentinel/internal/config"
	"sentinel/internal/engine"
)

// Restore restores one of the job's snapshots from the repository into targetDir.
// ref is a snapshot id, a unique id prefix or "latest". Only the job's own snapshots
// on this host are considered: another job's snapshot fails with restic.ErrForeignSnapshot.
//
// It returns the full snapshot id and, for a database job, the shell command that loads
// the restored dump ("" for a files job).
func (r *Runner) Restore(ctx context.Context, job *config.Job, repoName, ref, targetDir string) (snapID, loadHint string, err error) {
	if err := PrepareRestoreTarget(targetDir); err != nil {
		return "", "", err
	}
	client, err := r.Restic(repoName)
	if err != nil {
		return "", "", err
	}
	if ref == "latest" {
		snaps, err := client.Snapshots(ctx, JobTags(job))
		if err != nil {
			return "", "", err
		}
		if len(snaps) == 0 {
			return "", "", fmt.Errorf("no snapshots for job %q in %s", job.Name, repoName)
		}
		snapID = snaps[len(snaps)-1].ID
	} else {
		snap, err := client.JobSnapshot(ctx, JobTags(job), ref)
		if err != nil {
			return "", "", err
		}
		snapID = snap.ID
	}
	if err := client.Restore(ctx, snapID, targetDir); err != nil {
		return "", "", err
	}
	if dbEngine, ok := engine.For(job.Type); ok {
		loadHint = dbEngine.RestoreHint(job, targetDir)
	}
	return snapID, loadHint, nil
}
