// Package app is what Sentinel does for a person or a program that asks: start and stop a
// backup, list snapshots, restore, read and edit the config file, run the doctor. The CLI,
// the terminal manager and the HTTP API all call it, so none of them holds business logic.
package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/status"
	"sentinel/internal/store"
)

// Service works on one loaded config, the store beside it, and the config file it came from.
type Service struct {
	cfg        *config.Config
	st         *store.Store
	configPath string
}

// New returns the service for a loaded config. configPath is the file cfg was read from; the
// methods that edit the config write to it.
func New(cfg *config.Config, st *store.Store, configPath string) *Service {
	return &Service{cfg: cfg, st: st, configPath: configPath}
}

// Status is the report of `sentinel status`: jobs, uptime checks, open alerts, running backups.
func (s *Service) Status(ctx context.Context, now time.Time) (status.Report, error) {
	return status.Build(ctx, s.cfg, s.st, now)
}

// JobRepository is one backup that can be started: a job and one of its repositories.
type JobRepository struct{ Job, Repository string }

// JobRepositories are the backups the config describes, in config order.
func (s *Service) JobRepositories() []JobRepository {
	var all []JobRepository
	for _, job := range s.cfg.Jobs {
		for _, repo := range job.Repositories {
			all = append(all, JobRepository{Job: job.Name, Repository: repo})
		}
	}
	return all
}

// job is the job of that name; a non-empty repo must be one of its repositories.
func (s *Service) job(jobName, repo string) (*config.Job, error) {
	job, err := s.cfg.FindJob(jobName)
	if err != nil {
		return nil, fmt.Errorf("job %q %w", jobName, ErrNotFound)
	}
	if repo != "" && !slices.Contains(job.Repositories, repo) {
		return nil, fmt.Errorf("repository %q of job %q %w", repo, jobName, ErrNotFound)
	}
	return job, nil
}
