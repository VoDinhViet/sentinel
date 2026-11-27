package tui

import (
	"context"
	"time"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/configedit"
	"sentinel/agent/internal/doctor"
	"sentinel/agent/internal/fleet"
	"sentinel/agent/internal/restic"

	"sentinel/pkg/agentapi"
)

// jobRepository is one backup the manager can act on: a job and one of its repositories.
type jobRepository struct {
	job, repository string
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
	// host reads how the machine is doing (CPU, memory, disks, failed units).
	host func(ctx context.Context) app.HostReport
	// fleet asks the servers of the fleet file for their reports (the Fleet tab).
	fleet func(ctx context.Context) ([]fleet.ServerStatus, error)
	// fleetHost reads the machine of one server of the fleet (the screen of that server).
	fleetHost func(ctx context.Context, server fleet.Server) (agentapi.HostStatus, error)
	// initialize writes a first config file (`sentinel init`).
	initialize func(initial configedit.Initial) error
}

// newManageActions hands the manager the service, one action per field.
func newManageActions(svc *app.Service) manageActions {
	return manageActions{
		startBackup:      svc.StartBackup,
		stopBackup:       svc.RequestStop,
		snapshots:        svc.ListSnapshots,
		restore:          svc.Restore,
		listJobs:         svc.ListJobs,
		saveJob:          svc.SaveJob,
		deleteJob:        svc.DeleteJob,
		doctor:           svc.Doctor,
		overview:         svc.Overview,
		saveRepository:   svc.SaveRepository,
		deleteRepository: svc.DeleteRepository,
		saveUptime:       svc.SaveUptime,
		deleteUptime:     svc.DeleteUptime,
		saveNotifier:     svc.SaveNotifier,
		deleteNotifier:   svc.DeleteNotifier,
		saveSettings:     svc.SaveSettings,
		testConnection:   svc.TestConnection,
		testNotifier:     svc.TestNotifier,
		host:             svc.Host,
		fleet:            func(ctx context.Context) ([]fleet.ServerStatus, error) { return fleet.Poll(ctx, "") },
		fleetHost:        fleet.ReadHost,
	}
}
