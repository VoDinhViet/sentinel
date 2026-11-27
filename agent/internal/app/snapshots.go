package app

import (
	"context"
	"errors"
	"fmt"

	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/restic"
)

// ListSnapshots are the snapshots of a job in one repository, newest first.
func (s *Service) ListSnapshots(ctx context.Context, jobName, repo string) ([]restic.Snapshot, error) {
	job, err := s.job(jobName, repo)
	if err != nil {
		return nil, err
	}
	client, err := (&backup.Runner{Config: s.cfg}).Restic(repo)
	if err != nil {
		return nil, err
	}
	snaps, err := client.Snapshots(ctx, backup.JobTags(job))
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(snaps)-1; i < j; i, j = i+1, j-1 { // restic lists the oldest first
		snaps[i], snaps[j] = snaps[j], snaps[i]
	}
	return snaps, nil
}

// Restore puts a snapshot of the job into targetDir. For a database job it returns the command
// that loads the restored dump, or "".
func (s *Service) Restore(ctx context.Context, jobName, repo string, snap restic.Snapshot, targetDir string) (loadHint string, err error) {
	job, err := s.job(jobName, repo)
	if err != nil {
		return "", err
	}
	_, loadHint, err = (&backup.Runner{Config: s.cfg}).Restore(ctx, job, repo, snap.ID, targetDir)
	if errors.Is(err, restic.ErrForeignSnapshot) {
		return "", fmt.Errorf("snapshot %s không phải là snapshot của job %q trong %s", shortID(snap.ID), jobName, repo)
	}
	return loadHint, err
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
