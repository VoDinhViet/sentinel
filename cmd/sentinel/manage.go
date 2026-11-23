package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"sentinel/internal/restic"
	"sentinel/internal/status"
	"sentinel/internal/store"
)

const (
	manageRefresh = 5 * time.Second
	minColumn     = 6
)

func (a *app) manageCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "manage",
		Aliases: []string{"tui"},
		Short:   "Giao diện terminal: backup, snapshot, khôi phục, uptime check và cảnh báo",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return a.runManage(cmd) },
	}
}

// runManage opens the terminal manager on the loaded config.
func (a *app) runManage(cmd *cobra.Command) error {
	if !stdoutIsTerminal() {
		return fmt.Errorf("manage cần chạy trong terminal; trong script hãy dùng `sentinel status --output json`")
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	st, err := a.openStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := cmd.Context()
	load := func() (status.Report, error) { return status.Build(ctx, cfg, st, time.Now()) }
	model := newManageModel(ctx, load, newManageActions(cfg, st, a.configPath), configuredJobRepositories(cfg))
	_, err = tea.NewProgram(model, tea.WithContext(ctx)).Run()
	return err
}

func stdoutIsTerminal() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Severity of one manager line, shown as a colour.
type severity int

const (
	sevOK severity = iota
	sevWarn
	sevBad
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
	// entry says which part of the config a line of the Config tab stands for.
	entry configEntry
}

type manageTab struct {
	title   string
	headers []string
	lines   []manageLine
	// statusCol is the column coloured by severity; historyCol (-1 = none) shows line.history.
	statusCol, historyCol int
	// alarm colours the tab label red.
	alarm bool
}

type manageMode int

const (
	modeTable     manageMode = iota // the three tabs
	modeSnapshots                   // snapshots of the selected job and repository
	modeTarget                      // typing the directory to restore into
	modeConfirm                     // typing the job name to confirm a restore
	modeBusy                        // a backup, snapshot listing or restore is running
	modeResult                      // the outcome of a restore
	modeForm                        // adding or editing a database job
	modeDelete                      // typing the job name to confirm its removal
	modeHelp                        // the key list
	modeBrowse                      // choosing folders for a Paths field
)

type manageModel struct {
	ctx             context.Context
	load            func() (status.Report, error)
	actions         manageActions
	jobRepositories []jobRepository
	report          status.Report
	loadErr         error
	loaded          bool

	mode     manageMode
	tab      int
	cursor   int
	width    int
	height   int
	quitting bool

	// notice is the outcome of the last action, shown under the table.
	notice    string
	noticeBad bool

	snapshotJob, snapshotRepo string
	snapshots                 []restic.Snapshot // newest first
	snapshotCursor            int
	chosen                    restic.Snapshot
	target, confirm           string
	inputError                string
	targetOptions             []string // candidates the last Tab listed for the restore folder

	busyText   string
	busyStart  time.Time
	busyFrame  int
	cancelBusy context.CancelFunc

	resultText string
	resultBad  bool

	form       form
	browse     browseState
	deleteName string
	// deleteWhat is "job", "repository"...; deleteNote tells what stays; deleteAction removes it.
	deleteWhat, deleteNote string
	deleteAction           func() error

	config configData

	helpReturn manageMode // the screen the key list was opened from

	doctor doctorState

	hits *hitMap // where a click lands, rebuilt by every render
}

type (
	reportMsg struct {
		report status.Report
		err    error
	}
	refreshMsg    struct{}
	spinMsg       struct{}
	backupDoneMsg struct {
		job, repository string
		runs            []store.BackupRun
		err             error
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
)
