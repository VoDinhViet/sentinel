package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
)

// The sections of a job form, in the order they are drawn.
const (
	sectionBasic      = "BASIC"
	sectionConnection = "CONNECTION"
	sectionSchedule   = "SCHEDULE & RETENTION"
	sectionStorage    = "STORAGE"
	sectionAdvanced   = "ADVANCED"
)

type fieldKind int

const (
	fieldText      fieldKind = iota
	fieldNumber              // digits only
	fieldChoice              // one of choices, with ‹ ›
	fieldChecklist           // several of choices, each ticked or not
	fieldSchedule            // text with presets on ← →, and a plain-words preview
	fieldRetention           // five counts on one line
	fieldPaths               // comma separated folders, with suggestions
	fieldToggle              // a line that opens or closes the advanced fields below it
)

var retentionKeys = [5]string{"last", "daily", "weekly", "monthly", "yearly"}

// scheduleHint says what a schedule field accepts and which keys step through it.
const scheduleHint = `type it ("daily 2am", "weekly monday 3am", "every 6 hours", cron) · ← → change how often · alt+↑↓ time · alt+←→ weekday or day`

// formField is one input of a form.
type formField struct {
	key     string
	label   string
	hint    string
	kind    fieldKind
	value   string // text, number, choice and schedule
	choices []string
	ticked  []bool    // checklist: one per choice
	counts  [5]string // retention: last daily weekly monthly yearly
	sub     int       // checklist item or retention count under the cursor
	// placeholder is what an empty field stands for, shown dimmed.
	placeholder string
	fixed       bool // a choice that cannot be changed
	// complete makes Tab finish a path typed in the field (a Paths field always does);
	// options are the candidates the last Tab listed.
	complete bool
	options  []string
	// section groups the fields of a job form under a heading ("" for no group).
	section string
	// onField and only show the field only while the field named onField holds one of only.
	onField string
	only    []string
}

// formDefaults are the config's defaults, shown on the fields they fill in.
type formDefaults struct {
	schedule     string
	repositories []string
	retention    config.Retention
}

// formKind says what a form edits.
type formKind int

const (
	formJob formKind = iota
	formRepository
	formUptime
	formNotifier
	formSettings
	formInit // the first-run form of `sentinel init`
)

// form edits one entry of the config: a job (a database dump or a set of files), a
// repository, an uptime check, a notifier notifier or the general settings. original is the job's name when editing, "" when adding.
type form struct {
	kind         formKind
	title        string
	original     string
	passwordKept bool // the file holds a literal password that stays unless an env var is entered
	known        configedit.Known
	defaults     formDefaults
	fields       []formField
	focus        int
	advancedOpen bool            // the advanced section of a job form is unfolded
	touched      map[string]bool // fields the user has been in: only those show their problem
	attempted    bool            // a save was tried: every problem shows
	problem      string          // a problem of the whole save, not of one field

	// kept is what a repository form does not edit and must write back as it was.
	kept map[string]string
}

func newJobForm(job configedit.Job, original string, listing configedit.Listing) form {
	form := form{
		original:     original,
		passwordKept: job.PasswordLiteral,
		known:        configedit.Known{Repositories: listing.Repositories, InheritsRepositories: len(listing.InheritedRepositories) > 0},
		defaults:     formDefaults{schedule: listing.Defaults.Schedule, repositories: listing.InheritedRepositories, retention: listing.Defaults.Retention},
		touched:      map[string]bool{},
	}
	port := ""
	if job.Port != 0 {
		port = strconv.Itoa(job.Port)
	}
	typeHint := "← → to choose"
	if original != "" {
		typeHint = "fixed for an existing job"
	}
	passwordHint := "name of an environment variable holding the password (never the password itself)"
	if job.PasswordLiteral {
		passwordHint = "the file has a literal password; enter a variable name to replace it, or leave empty to keep it"
	}
	scheduleHint := scheduleHint
	scheduleNote := "empty = only run by hand"
	if listing.Defaults.Schedule != "" {
		scheduleHint += "; empty = the default schedule"
		scheduleNote = "default: " + listing.Defaults.Schedule
	}
	ticked := make([]bool, len(listing.Repositories))
	for i, name := range listing.Repositories {
		for _, own := range job.Repositories {
			ticked[i] = ticked[i] || own == name
		}
	}
	repositoriesHint := "space or click ticks a repository"
	if len(listing.InheritedRepositories) > 0 {
		repositoriesHint += "; none ticked = " + strings.Join(listing.InheritedRepositories, ", ") + " (the default)"
	}
	retention := [5]string{}
	for i, count := range []int{job.Retention.Last, job.Retention.Daily, job.Retention.Weekly, job.Retention.Monthly, job.Retention.Yearly} {
		if count > 0 {
			retention[i] = strconv.Itoa(count)
		}
	}
	form.kind, form.title = formJob, "Add a job"
	if original != "" {
		form.title = "Edit " + original
	}
	form.fields = []formField{
		{key: "type", label: "Type", hint: typeHint, kind: fieldChoice, value: job.Type, choices: configedit.JobTypes, fixed: original != "", section: sectionBasic},
		{key: "name", label: "Name", hint: "letters, digits, . _ -", value: job.Name, section: sectionBasic},
		{key: "paths", label: "Paths", hint: "files and folders to back up, separated by commas; ${HOME} works; Tab completes, alt+1.. or a click adds a suggested folder, ctrl+o browses the server", kind: fieldPaths, value: strings.Join(job.Paths, ", "), section: sectionBasic},
		{key: "database", label: "Database", hint: "the database to dump", value: job.Database, section: sectionBasic},
		{key: "host", label: "Host", hint: "default localhost", value: job.Host, placeholder: "localhost", section: sectionConnection},
		{key: "port", label: "Port", hint: "default 5432 / 3306 / 27017", kind: fieldNumber, value: port, placeholder: "default", section: sectionConnection},
		{key: "user", label: "User", hint: "optional", value: job.User, section: sectionConnection},
		{key: "password_env", label: "Password env var", hint: passwordHint, value: job.PasswordEnv, section: sectionConnection},
		{key: "schedule", label: "Schedule", hint: scheduleHint, kind: fieldSchedule, value: job.Schedule, placeholder: scheduleNote, section: sectionSchedule},
		{key: "retention", label: "Keep", hint: "how many snapshots to keep: last N, then one per day, week, month, year; all empty = keep everything", kind: fieldRetention, counts: retention, section: sectionSchedule},
		{key: "store_at", label: "Folder", hint: "a folder for this job's backups (tab completes it); a repository named like the job is created for it. Empty = the repositories ticked below, or, for a database job with none, an automatic folder " + autoStoreBase() + "/<job name>", complete: true, placeholder: "automatic", section: sectionStorage},
		{key: "repositories", label: "Repositories", hint: repositoriesHint, kind: fieldChecklist, choices: listing.Repositories, ticked: ticked, section: sectionStorage},
		{key: "advanced", label: "Advanced options", hint: "enter or space opens or closes the options below", kind: fieldToggle, section: sectionAdvanced},
		{key: "exclude", label: "Exclude", hint: "patterns to skip, separated by commas, e.g. *.tmp, node_modules", value: strings.Join(job.Exclude, ", "), section: sectionAdvanced},
		{key: "tls", label: "TLS", hint: "mysql only: empty, disabled or required", value: job.TLS, section: sectionAdvanced},
		{key: "auth_database", label: "Auth database", hint: "mongodb only: where the user is defined", value: job.AuthDatabase, section: sectionAdvanced},
	}
	form.advancedOpen = len(job.Exclude) > 0 || job.TLS != "" || job.AuthDatabase != "" // keep what is set in view
	for i := range form.fields {
		if types, restricted := jobFieldTypes[form.fields[i].key]; restricted {
			form.fields[i].onField, form.fields[i].only = "type", types
		}
	}
	return form
}

// jobFieldTypes are the job types each type-specific field applies to; the others apply to all.
var jobFieldTypes = map[string][]string{
	"paths":         {config.JobFiles},
	"exclude":       {config.JobFiles},
	"advanced":      {config.JobFiles, config.JobMySQL, config.JobMongoDB}, // postgres has nothing advanced
	"database":      configedit.DatabaseTypes,
	"host":          configedit.DatabaseTypes,
	"port":          configedit.DatabaseTypes,
	"user":          configedit.DatabaseTypes,
	"password_env":  configedit.DatabaseTypes,
	"tls":           {config.JobMySQL},
	"auth_database": {config.JobMongoDB},
}

// visible are the fields that apply right now (to the chosen type or kind).
func (f form) visible() []int {
	var shown []int
	for i, field := range f.fields {
		if field.onField != "" && !slices.Contains(field.only, f.field(field.onField).value) {
			continue
		}
		if field.section == sectionAdvanced && field.kind != fieldToggle && !f.advancedOpen {
			continue
		}
		shown = append(shown, i)
	}
	return shown
}

// splitList splits a comma separated list, dropping blanks.
func splitList(text string) []string {
	var parts []string
	for _, part := range strings.Split(text, ",") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func (f form) field(key string) *formField {
	for i := range f.fields {
		if f.fields[i].key == key {
			return &f.fields[i]
		}
	}
	return &formField{}
}

// formProblem is a mistake in what was typed into the form, shown under that field.
func formProblem(field, format string, args ...any) error {
	return &configedit.InvalidError{Message: fmt.Sprintf(format, args...), Field: field}
}

// job turns the form into a job, or says what is wrong with an input.
func (f form) job() (configedit.Job, error) {
	job := configedit.Job{
		Type:         f.field("type").value,
		Name:         strings.TrimSpace(f.field("name").value),
		Database:     strings.TrimSpace(f.field("database").value),
		Host:         strings.TrimSpace(f.field("host").value),
		User:         strings.TrimSpace(f.field("user").value),
		PasswordEnv:  strings.TrimSpace(f.field("password_env").value),
		Schedule:     strings.TrimSpace(f.field("schedule").value),
		TLS:          strings.TrimSpace(f.field("tls").value),
		AuthDatabase: strings.TrimSpace(f.field("auth_database").value),

		Paths:           splitList(f.field("paths").value),
		Exclude:         splitList(f.field("exclude").value),
		PasswordLiteral: f.passwordKept,
	}
	if portText := strings.TrimSpace(f.field("port").value); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil {
			return job, formProblem("port", "port: %q is not a number", portText)
		}
		job.Port = port
	}
	repositories := f.field("repositories")
	for i, name := range repositories.choices {
		if repositories.ticked[i] {
			job.Repositories = append(job.Repositories, name)
		}
	}
	counts := f.field("retention").counts
	for i, target := range []*int{&job.Retention.Last, &job.Retention.Daily, &job.Retention.Weekly, &job.Retention.Monthly, &job.Retention.Yearly} {
		if counts[i] == "" {
			continue
		}
		count, err := strconv.Atoi(counts[i])
		if err != nil {
			return job, formProblem("retention", "retention: %q is not a number", counts[i])
		}
		*target = count
	}
	return job, nil
}

// problems is what is wrong with the form right now, by field key ("" for no field).
func (f form) problems() map[string]string {
	found := map[string]string{}
	add := func(err error) {
		var invalid *configedit.InvalidError
		if errors.As(err, &invalid) {
			if _, seen := found[invalid.Field]; !seen {
				found[invalid.Field] = invalid.Message
			}
		}
	}
	if f.kind != formJob {
		for _, err := range f.checkEntries() {
			add(err)
		}
		return found
	}
	job, known, _, err := f.jobWithStore()
	add(err)
	for _, problem := range configedit.Problems(&job, known) {
		add(problem)
	}
	return found
}

// shownProblems are the problems of the fields the user has been in (all after a failed save).
func (f form) shownProblems() map[string]string {
	shown := map[string]string{}
	for field, message := range f.problems() {
		if f.attempted || f.touched[field] {
			shown[field] = message
		}
	}
	return shown
}

// completes says whether Tab finishes a path in the field.
func (f formField) completes() bool { return f.kind == fieldPaths || f.complete }

// completeFocused finishes the path in the field on Tab. It reports whether it did anything
// (changed the text or listed candidates it had not listed yet), so Tab moves on otherwise.
func (f *form) completeFocused() bool {
	field := &f.fields[f.focus]
	if !field.completes() || field.fixed {
		return false
	}
	completed, options := completeList(field.value, field.kind == fieldPaths)
	listed := len(options) > 0 && !slices.Equal(options, field.options)
	changed := completed != field.value
	field.value, field.options = completed, options
	f.touched[field.key] = true
	return changed || listed
}

// storeAt is the new folder typed in the "Store at" field of a job form, "" when it is empty.
func (f form) storeAt() string {
	if f.kind != formJob {
		return ""
	}
	return strings.TrimSpace(f.field("store_at").value)
}

// jobWithStore is the job of the form plus the repository its "Store at" folder will create
// (named like the job), and the repositories the job may refer to once that one exists.
func (f form) jobWithStore() (configedit.Job, configedit.Known, configedit.Repository, error) {
	job, err := f.job()
	known := f.known
	folder := f.storeAt()
	if folder == "" {
		folder = f.autoFolder(job)
	}
	if folder == "" {
		return job, known, configedit.Repository{}, err
	}
	repo := configedit.Repository{Name: job.Name, URL: folder, PasswordEnv: "RESTIC_PASSWORD"}
	if slices.Contains(f.known.Repositories, repo.Name) {
		return job, known, repo, &configedit.InvalidError{Field: "store_at", Message: fmt.Sprintf("a repository named %q exists: tick it below instead, or give the job another name", repo.Name)}
	}
	if problem := configedit.ValidateRepository(repo); problem != nil && job.Name != "" {
		var invalid *configedit.InvalidError
		if errors.As(problem, &invalid) && invalid.Field != "password_env" {
			return job, known, repo, &configedit.InvalidError{Field: "store_at", Message: invalid.Message}
		}
	}
	known.Repositories = append(slices.Clone(known.Repositories), repo.Name)
	job.Repositories = append(job.Repositories, repo.Name)
	return job, known, repo, err
}

// autoStoreBase is the folder under which Sentinel puts the repository of a database job that
// was given no place to store its backups: one folder per job.
func autoStoreBase() string {
	if os.Geteuid() == 0 {
		return "/var/backups/sentinel"
	}
	return "${HOME}/.local/share/sentinel"
}

// autoFolder is the folder Sentinel builds for a database job that has no repository (none
// ticked, none inherited from the defaults) and no "Store at" folder: <base>/<job name>. It is
// "" for any other job.
func (f form) autoFolder(job configedit.Job) string {
	if f.kind != formJob || job.Name == "" || !slices.Contains(configedit.DatabaseTypes, job.Type) ||
		len(job.Repositories) > 0 || f.known.InheritsRepositories ||
		configedit.ValidateRepository(configedit.Repository{Name: job.Name, URL: "x", PasswordEnv: "X"}) != nil { // a bad name is the name field's problem
		return ""
	}
	return autoStoreBase() + "/" + job.Name
}
