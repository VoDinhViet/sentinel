package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
)

// initPath is where `sentinel init` writes: --config or $SENTINEL_CONFIG when given, the system
// path for root, the per-user path otherwise.
func initPath(locations configLocations, root bool) string {
	switch {
	case locations.flagPath != "":
		return locations.flagPath
	case locations.envPath != "":
		return locations.envPath
	case root || locations.user == "":
		return locations.system
	}
	return locations.user
}

// initOutcome is what a finished init tells the user.
type initOutcome struct {
	path, envFile string
	passwordEnv   string
	secretWritten bool
	jobName       string
	repoName      string
}

func (a *app) initCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a first config file by answering a form",
		Long: "Asks where to keep the backups and what to back up, then writes a short config file.\n" +
			"Root writes /etc/sentinel/config.yaml (and a random repository password into sentinel.env);\n" +
			"anyone else writes ~/.config/sentinel/config.yaml. --config or $SENTINEL_CONFIG choose another file.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root := os.Geteuid() == 0
			path := initPath(currentConfigLocations(a.configFlag), root)
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists: edit it with `sentinel manage`, or use --force to replace it (the old one is kept as %s.old)", path, path)
			}
			outcome, err := runInit(cmd.Context(), path, root, force)
			if err != nil {
				return err
			}
			if outcome == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "Nothing written.")
				return nil
			}
			printInitOutcome(cmd.OutOrStdout(), outcome)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing config file (the old one is kept as .old)")
	return cmd
}

// runInit shows the form and writes the config; it returns nil when the user cancelled.
func runInit(ctx context.Context, path string, root, force bool) (*initOutcome, error) {
	hostname, _ := os.Hostname()
	outcome := &initOutcome{path: path}
	actions := manageActions{initialize: func(initial configedit.Initial) error {
		if err := configedit.Create(path, initial, force); err != nil {
			return err
		}
		outcome.jobName, outcome.repoName, outcome.passwordEnv = initial.Job.Name, initial.Repository.Name, initial.Repository.PasswordEnv
		// A server install keeps secrets in sentinel.env beside the config: give the repository a password there.
		if root && filepath.Dir(path) == filepath.Dir(systemConfigPath) {
			outcome.envFile = filepath.Join(filepath.Dir(path), "sentinel.env")
			written, err := configedit.EnsureSecret(outcome.envFile, initial.Repository.PasswordEnv)
			if err != nil {
				return fmt.Errorf("the config was written, but not the repository password: %w", err)
			}
			outcome.secretWritten = written
		}
		return nil
	}}
	model := newManageModel(ctx, func() (statusReport, error) { return statusReport{Server: hostname}, nil }, actions, nil)
	model.loaded = true
	model.report.Server = hostname
	model = model.openEntryForm(newInitForm(path, root, hostname))
	final, err := tea.NewProgram(initProgram{manageModel: model}, tea.WithContext(ctx)).Run()
	if err != nil {
		return nil, err
	}
	program := final.(initProgram)
	switch {
	case program.noticeBad:
		return nil, errors.New(program.notice)
	case program.saved:
		return outcome, nil
	}
	return nil, nil
}

// initProgram is the manage screen reduced to one form: it ends when the form is saved or cancelled.
type initProgram struct {
	manageModel
	saved bool
}

func (p initProgram) Init() tea.Cmd { return nil }

func (p initProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return p.after(p.manageModel.Update(msg))
}

// after takes what the screen did and ends the program once the form is gone.
func (p initProgram) after(model tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	p.manageModel = model.(manageModel)
	if p.mode != modeForm {
		p.saved = !p.noticeBad && p.notice != ""
		return p, tea.Quit
	}
	return p, cmd
}

func printInitOutcome(w io.Writer, outcome *initOutcome) {
	fmt.Fprintf(w, "Wrote %s\n\n", outcome.path)
	switch {
	case outcome.secretWritten:
		fmt.Fprintf(w, "A random repository password was written to %s (mode 0600) as %s.\n"+
			"Keep a copy somewhere else: losing it means losing every backup.\n\n", outcome.envFile, outcome.passwordEnv)
		fmt.Fprintf(w, "Next:\n  set -a; . %s; set +a\n", outcome.envFile)
	case os.Getenv(outcome.passwordEnv) == "":
		fmt.Fprintf(w, "Next:\n  export %s='a-long-passphrase'   # losing it means losing the backups\n", outcome.passwordEnv)
	default:
		fmt.Fprintln(w, "Next:")
	}
	fmt.Fprintf(w, "  sentinel backup init %s     # creates the repository, once\n", outcome.jobName)
	fmt.Fprintf(w, "  sentinel backup run %s      # backs up now\n", outcome.jobName)
	fmt.Fprintln(w, "  sentinel doctor")
	fmt.Fprintln(w, "  sentinel manage             # edit everything from the terminal")
}

// ---- the form

var initJobTypes = []string{config.BackupFiles, config.BackupPostgres, config.BackupMySQL, config.BackupMongoDB}

// newInitForm is the repository form plus the first job and its schedule, on one screen.
func newInitForm(path string, root bool, hostname string) form {
	repoLocation, jobPaths, store := "${HOME}/.local/share/sentinel/repo", "${HOME}/Documents", "${HOME}/.local/state/sentinel/state.db"
	if root {
		repoLocation, jobPaths, store = "/var/backups/sentinel", "/etc, /var/www", "" // the default, /var/lib/sentinel/state.db
	}
	repo := newRepositoryForm(configedit.Repository{
		Name: "local", URL: repoLocation, PasswordEnv: "RESTIC_PASSWORD",
		Env: map[string]string{awsKeyID: awsKeyID, awsSecretKey: awsSecretKey},
	}, "", configedit.Listing{})
	jobTypeOnly := func(types ...string) (string, []string) { return "job_type", types }
	jobField := func(key, label, hint, value string, types ...string) formField {
		field := formField{key: key, label: label, hint: hint, value: value}
		if len(types) > 0 {
			field.onField, field.only = jobTypeOnly(types...)
		}
		return field
	}
	fields := append([]formField{
		{key: "server_name", label: "Server name", hint: "shown in alerts and on the screen", value: hostname},
		{key: "store", label: "State database", hint: "where run history and job locks are kept (a file path)", value: store, placeholder: "/var/lib/sentinel/state.db"},
	}, repo.fields...)
	for i := range fields {
		switch fields[i].key {
		case "name":
			fields[i].label, fields[i].hint = "Repository name", "letters, digits, . _ - ; the first backup repository"
		case "kind":
			fields[i].label, fields[i].hint = "Store backups", "← → to choose where the backups go; you can add more repositories later"
		}
	}
	fields = append(fields,
		formField{key: "job_type", label: "Back up", hint: "← → files and folders, or a database dump", kind: fieldChoice, value: config.BackupFiles, choices: initJobTypes},
		jobField("job_name", "Job name", "letters, digits, . _ -", "documents"),
		jobField("job_paths", "Paths", "files and folders to back up, separated by commas; ${HOME} works; alt+1.. or a click adds a suggested folder", jobPaths, config.BackupFiles),
		jobField("job_database", "Database", "the database to dump", "", configedit.DatabaseTypes...),
		jobField("job_password_env", "DB password var", "name of the environment variable holding the database password; empty if none", "", configedit.DatabaseTypes...),
		formField{key: "job_schedule", label: "Schedule", hint: "daily 02:00 · weekly sun 03:00 · monthly 1 04:00 · hourly :15 · every 6h · a cron expression · manual; ← → for presets", kind: fieldSchedule, value: "daily 02:00"},
		formField{key: "retention", label: "Keep", hint: "how many snapshots to keep: last N, then one per day, week, month, year", kind: fieldRetention, counts: [5]string{"", "7", "4", "6", ""}},
	)
	for i := range fields {
		if fields[i].key == "job_paths" {
			fields[i].kind = fieldPaths
		}
		if fields[i].key == "job_database" || fields[i].key == "job_password_env" {
			fields[i].onField, fields[i].only = "job_type", configedit.DatabaseTypes
		}
	}
	return form{kind: formInit, title: "Create " + path, touched: map[string]bool{}, fields: fields}
}

// initial turns the form into what configedit.Create needs.
func (f form) initial() (configedit.Initial, error) {
	repo := f.repository()
	job := configedit.Job{
		Name: strings.TrimSpace(f.field("job_name").value), Type: f.field("job_type").value,
		Paths: splitList(f.field("job_paths").value), Database: strings.TrimSpace(f.field("job_database").value),
		PasswordEnv: strings.TrimSpace(f.field("job_password_env").value),
	}
	if job.Type != config.BackupFiles {
		job.Paths = nil
	}
	var retention config.Retention
	for i, target := range []*int{&retention.Last, &retention.Daily, &retention.Weekly, &retention.Monthly, &retention.Yearly} {
		if text := f.field("retention").counts[i]; text != "" {
			if _, err := fmt.Sscan(text, target); err != nil {
				return configedit.Initial{}, formProblem("retention", "retention: %q is not a number", text)
			}
		}
	}
	return configedit.Initial{
		ServerName: strings.TrimSpace(f.field("server_name").value), Store: strings.TrimSpace(f.field("store").value), Repository: repo, Job: job,
		Schedule: strings.TrimSpace(f.field("job_schedule").value), Retention: retention,
	}, nil
}

// initProblems validates the init form without writing anything.
func (f form) initProblems() []error {
	initial, err := f.initial()
	if err != nil {
		return []error{err}
	}
	problems := f.repositoryProblems(initial.Repository)
	jobKeys := map[string]string{"name": "job_name", "type": "job_type", "paths": "job_paths", "database": "job_database", "password_env": "job_password_env", "schedule": "job_schedule"}
	job := initial.Job
	job.Schedule = initial.Schedule
	known := configedit.Known{Repositories: []string{initial.Repository.Name}, InheritsRepositories: true}
	for _, problem := range configedit.Problems(&job, known) {
		key := jobKeys[problem.Field]
		if key == "" {
			key = problem.Field
		}
		problems = append(problems, formProblem(key, "%s", problem.Message))
	}
	return problems
}
