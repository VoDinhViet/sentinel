package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"sentinel/internal/backup"
	"sentinel/internal/config"
	"sentinel/internal/configedit"
	"sentinel/internal/engine"
	"sentinel/internal/notify"
	"sentinel/internal/restic"
	"sentinel/internal/store"

	"sentinel/internal/doctor"
)

// connectTimeout is how long a connection test waits for the database to answer.
const connectTimeout = 10 * time.Second

// jobRepository is one backup the manager can act on: a job and one of its repositories.
type jobRepository struct {
	job, repository string
}

func configuredJobRepositories(cfg *config.Config) []jobRepository {
	var jobRepositories []jobRepository
	for _, job := range cfg.Jobs {
		for _, repository := range job.Repositories {
			jobRepositories = append(jobRepositories, jobRepository{job: job.Name, repository: repository})
		}
	}
	return jobRepositories
}

// manageActions are the things the manager does besides reading the store. They
// are fields so tests can replace them.
type manageActions struct {
	// startBackup runs a backup in a process of its own, so it goes on when the manager is left;
	// stopBackup asks the running backup of a job to stop.
	startBackup func(job, repository string) (pid int, logPath string, err error)
	stopBackup  func(job string) error
	snapshots   func(ctx context.Context, job, repository string) ([]restic.Snapshot, error)
	// restore returns the command that loads a restored database dump, or "".
	restore func(ctx context.Context, job, repository string, snapshot restic.Snapshot, target string) (string, error)
	// The config file: read the jobs, save a database job, remove one.
	listJobs  func() (configedit.Listing, error)
	saveJob   func(original string, job configedit.Job) error
	deleteJob func(name string) error
	// doctor runs the same checks as `sentinel doctor`.
	doctor func(ctx context.Context) []doctor.Finding
	// The rest of the config file, shown and edited on the Config tab.
	overview         func() (configedit.Overview, error)
	saveRepository   func(original string, repository configedit.Repository) error
	deleteRepository func(name string) error
	saveUptime       func(original string, check configedit.Uptime) error
	deleteUptime     func(name string) error
	saveNotifier     func(notifier configedit.Notifier) error
	deleteNotifier   func(kind string) error
	saveSettings     func(settings configedit.Settings) error
	// testConnection tries to reach the database of a database job as the form holds it. original
	// is the name the job has in the config file ("" for a new job): a password written in the file
	// is taken from there.
	testConnection func(ctx context.Context, job configedit.Job, original string) (time.Duration, error)
	// testNotifier sends a test message through the notifier of that kind, as the config file
	// has it now (a notifier added a minute ago works without restarting the manager).
	testNotifier func(ctx context.Context, kind string) error
	// initialize writes a first config file (`sentinel init`).
	initialize func(initial configedit.Initial) error
}

func newManageActions(cfg *config.Config, st *store.Store, configPath string) manageActions {
	jobAndRestic := func(jobName, repository string) (*config.Job, *restic.Client, error) {
		job, err := cfg.FindJob(jobName)
		if err != nil {
			return nil, nil, err
		}
		client, err := (&backup.Runner{Config: cfg}).Restic(repository)
		return job, client, err
	}
	return manageActions{
		listJobs:  func() (configedit.Listing, error) { return configedit.List(configPath) },
		saveJob:   func(original string, job configedit.Job) error { return configedit.Save(configPath, original, job) },
		deleteJob: func(name string) error { return configedit.Delete(configPath, name) },
		overview:  func() (configedit.Overview, error) { return configedit.Read(configPath) },
		saveRepository: func(original string, repository configedit.Repository) error {
			return configedit.SaveRepository(configPath, original, repository)
		},
		deleteRepository: func(name string) error { return configedit.DeleteRepository(configPath, name) },
		saveUptime: func(original string, check configedit.Uptime) error {
			return configedit.SaveUptime(configPath, original, check)
		},
		deleteUptime:   func(name string) error { return configedit.DeleteUptime(configPath, name) },
		saveNotifier:   func(notifier configedit.Notifier) error { return configedit.SaveNotifier(configPath, notifier) },
		deleteNotifier: func(kind string) error { return configedit.DeleteNotifier(configPath, kind) },
		saveSettings:   func(settings configedit.Settings) error { return configedit.SaveSettings(configPath, settings) },
		testNotifier: func(ctx context.Context, kind string) error {
			refreshEnv(configPath)
			return testNotifier(ctx, configPath, cfg.ServerName, kind)
		},
		testConnection: func(ctx context.Context, job configedit.Job, original string) (time.Duration, error) {
			refreshEnv(configPath)
			saved := ""
			if job.PasswordLiteral && original != "" {
				saved = configedit.SavedPassword(configPath, original)
			}
			probeJob, err := configedit.ConnectionJob(job, saved, os.LookupEnv)
			if err != nil {
				return 0, err
			}
			ctx, cancel := context.WithTimeout(ctx, connectTimeout)
			defer cancel()
			start := time.Now()
			err = engine.Probe(ctx, probeJob, cfg.EnvRefs)
			return time.Since(start), err
		},
		doctor: func(ctx context.Context) []doctor.Finding {
			return doctor.Run(ctx, cfg, configPath, func(cfg *config.Config) (*store.Store, error) { return store.Open(cfg.StorePath) })
		},
		startBackup: func(jobName, repository string) (int, string, error) {
			job, err := cfg.FindJob(jobName)
			if err != nil {
				return 0, "", err
			}
			if _, err := job.OnlyRepo(repository); err != nil {
				return 0, "", err
			}
			return startDetachedBackup(cfg, configPath, job.Name, repository)
		},
		stopBackup: func(jobName string) error {
			return backup.RequestStop(backup.ProgressDir(cfg.StorePath), jobName, time.Now())
		},
		snapshots: func(ctx context.Context, jobName, repository string) ([]restic.Snapshot, error) {
			job, client, err := jobAndRestic(jobName, repository)
			if err != nil {
				return nil, err
			}
			snapshots, err := client.Snapshots(ctx, backup.JobTags(job))
			if err != nil {
				return nil, err
			}
			for i, j := 0, len(snapshots)-1; i < j; i, j = i+1, j-1 { // newest first
				snapshots[i], snapshots[j] = snapshots[j], snapshots[i]
			}
			return snapshots, nil
		},
		restore: func(ctx context.Context, jobName, repository string, snapshot restic.Snapshot, target string) (string, error) {
			job, err := cfg.FindJob(jobName)
			if err != nil {
				return "", err
			}
			_, loadHint, err := (&backup.Runner{Config: cfg}).Restore(ctx, job, repository, snapshot.ID, target)
			if errors.Is(err, restic.ErrForeignSnapshot) {
				return "", fmt.Errorf("snapshot %s không phải là snapshot của job %q trong %s", shortID(snapshot.ID), jobName, repository)
			}
			return loadHint, err
		},
	}
}

// testNotifier sends one test message through the notifier of the given kind, read from the
// config file. Only that notifier's own ${VAR}s need to be set.
func testNotifier(ctx context.Context, configPath, server, kind string) error {
	only, err := config.LoadNotifier(configPath, kind)
	if err != nil {
		return err
	}
	multi := notify.NewMulti(only)
	if len(multi.Notifiers) == 0 {
		return fmt.Errorf("notifier %s is not in the config file", kind)
	}
	multi.Attempts = 1
	errs := multi.SendEach(ctx, notify.Message{
		Level: notify.Info, Server: server, Title: "Test notification",
		Body: "Sent from `sentinel manage` at " + time.Now().Format(time.RFC3339),
	})
	return errors.Join(errs...)
}
