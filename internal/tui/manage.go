package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/app"
	"sentinel/internal/restic"
	"sentinel/internal/status"
)

const (
	manageRefresh        = 5 * time.Second
	manageRefreshRunning = 2 * time.Second
	minColumn            = 6
)

// RunManage opens the terminal manager on svc and returns when the user leaves it.
func RunManage(ctx context.Context, svc *app.Service) error {
	load := func() (status.Report, error) { return svc.Status(ctx, time.Now()) }
	model := newManageModel(ctx, load, newManageActions(svc), jobRepositoriesOfService(svc))
	_, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	return err
}

// jobRepositoriesOfService are the backups the config describes, for the lines of jobs that never ran.
func jobRepositoriesOfService(svc *app.Service) []jobRepository {
	var jobRepos []jobRepository
	for _, backup := range svc.JobRepositories() {
		jobRepos = append(jobRepos, jobRepository{job: backup.Job, repository: backup.Repository})
	}
	return jobRepos
}

// Severity of one manager line, shown as a colour.
type severity int

const (
	sevOK severity = iota
	sevWarn
	sevBad
	sevRun // a backup that is running now
)

// manageLine is one table line of a manager tab. detail is the full text shown
// under the table for the selected line, since cells are shortened to fit.
type manageLine struct {
	cells    []string
	severity severity
	detail   string
	// job and repository identify the backup a line stands for (Backups tab only).
	job, repository string
	// history is the outcome of the latest runs, oldest first (Backups tab only).
	history []string
	// running says a backup of this job and repository is in progress (Backups tab only).
	running bool
	// entry says which part of the config a line of the Config tab stands for.
	entry configEntry
	// placeholder marks the row that invites adding the first entry of an empty group (Config tab).
	placeholder bool
	// sub is the dim line under the row ("└ the full error", the progress of a running backup).
	sub string
	// fields are the label/value pairs the detail panel shows for the line; without them the
	// panel shows detail.
	fields [][2]string
}

type manageTab struct {
	title   string
	headers []string
	lines   []manageLine
	// sections split the lines into groups with their own heading and column headers (the
	// Config tab); without them the tab is one table under headers.
	sections []tableSection
	// count is the number next to the tab's title: how many lines, or how many problems.
	count int
	// statusCol is the column coloured by severity; historyCol (-1 = none) shows line.history.
	statusCol, historyCol int
	// alarm colours the tab label red.
	alarm bool
}

// tableSection is a run of lines that starts at line index first and ends where the next
// section starts. title and hint make the heading row; headers name the columns of the run.
type tableSection struct {
	title, hint string
	headers     []string
	first       int
}

type manageModel struct {
	ctx             context.Context
	load            func() (status.Report, error)
	actions         manageActions
	jobRepositories []jobRepository
	report          status.Report
	loadErr         error
	loaded          bool

	screens  []screen // opened over the tabs, the last on show; see screen.go
	tab      int
	cursor   int
	width    int
	height   int
	quitting bool

	// notice is the outcome of the last action, shown under the table.
	notice    string
	noticeBad bool

	// stopAsked is the job whose stop was asked for once; a second x confirms it.
	stopAsked string

	connectSeq int // the number of connection tests started, so a late answer to an old one is ignored

	form   form
	browse browseState

	config configData

	doctor doctorState

	hits *hitMap // where a click lands, rebuilt by every render
}

type (
	reportMsg struct {
		report status.Report
		err    error
	}
	refreshMsg       struct{}
	spinMsg          struct{}
	backupStartedMsg struct {
		job, repository string
		pid             int
		logPath         string
		err             error
	}
	backupStopRequestedMsg struct {
		job string
		err error
	}
	snapshotsMsg struct {
		job, repository string
		snapshots       []restic.Snapshot
		err             error
	}
	restoreDoneMsg struct {
		hint string
		err  error
	}
	notifierTestedMsg struct {
		kind string
		err  error
	}
	connectTestedMsg struct {
		seq  int
		took time.Duration
		err  error
	}
)
