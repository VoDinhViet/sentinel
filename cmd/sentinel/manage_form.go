package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
)

type fieldKind int

const (
	fieldText fieldKind = iota
	fieldChoice
)

// formField is one input of the database job form.
type formField struct {
	key     string
	label   string
	hint    string
	kind    fieldKind
	value   string
	choices []string // for fieldChoice
}

// jobForm edits one database job. original is the job's name when editing, "" when adding.
type jobForm struct {
	original     string
	passwordKept bool // the file holds a literal password that stays unless an env var is entered
	repositories []string
	fields       []formField
	focus        int
	problem      string
}

func newJobForm(job configedit.Job, original string, repositories []string) jobForm {
	form := jobForm{original: original, passwordKept: job.PasswordLiteral, repositories: repositories}
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
	form.fields = []formField{
		{key: "type", label: "Type", hint: typeHint, kind: fieldChoice, value: job.Type, choices: configedit.DatabaseTypes},
		{key: "name", label: "Name", hint: "letters, digits, . _ -", value: job.Name},
		{key: "database", label: "Database", hint: "the database to dump", value: job.Database},
		{key: "host", label: "Host", hint: "default localhost", value: job.Host},
		{key: "port", label: "Port", hint: "default 5432 / 3306 / 27017", value: port},
		{key: "user", label: "User", hint: "optional", value: job.User},
		{key: "password_env", label: "Password env var", hint: passwordHint, value: job.PasswordEnv},
		{key: "schedule", label: "Schedule", hint: `cron, e.g. "0 3 * * *"; empty = only run by hand`, value: job.Schedule},
		{key: "repositories", label: "Repositories", hint: "comma separated, from: " + strings.Join(repositories, ", "), value: strings.Join(job.Repositories, ", ")},
		{key: "retention", label: "Retention", hint: "e.g. daily=7 weekly=4 monthly=6 (keys: last daily weekly monthly yearly); empty = keep all", value: retentionText(job.Retention)},
		{key: "tls", label: "TLS", hint: "mysql only: empty, disabled or required", value: job.TLS},
		{key: "auth_database", label: "Auth database", hint: "mongodb only: where the user is defined", value: job.AuthDatabase},
	}
	return form
}

// visible are the fields that apply to the chosen type.
func (f jobForm) visible() []int {
	jobType := f.field("type").value
	var shown []int
	for i, field := range f.fields {
		switch {
		case field.key == "tls" && jobType != config.BackupMySQL:
		case field.key == "auth_database" && jobType != config.BackupMongoDB:
		default:
			shown = append(shown, i)
		}
	}
	return shown
}

func (f jobForm) field(key string) *formField {
	for i := range f.fields {
		if f.fields[i].key == key {
			return &f.fields[i]
		}
	}
	return &formField{}
}

// formProblem is a mistake in what was typed into the form, shown inside the form.
func formProblem(format string, args ...any) error {
	return &configedit.InvalidError{Message: fmt.Sprintf(format, args...)}
}

func retentionText(r config.Retention) string {
	var parts []string
	for _, field := range []struct {
		key   string
		value int
	}{{"last", r.Last}, {"daily", r.Daily}, {"weekly", r.Weekly}, {"monthly", r.Monthly}, {"yearly", r.Yearly}} {
		if field.value > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", field.key, field.value))
		}
	}
	return strings.Join(parts, " ")
}

func parseRetention(text string) (config.Retention, error) {
	var retention config.Retention
	for _, part := range strings.Fields(strings.ReplaceAll(text, ",", " ")) {
		key, value, found := strings.Cut(part, "=")
		count, err := strconv.Atoi(value)
		if !found || err != nil || count < 0 {
			return retention, formProblem("retention: %q is not like daily=7", part)
		}
		switch key {
		case "last":
			retention.Last = count
		case "daily":
			retention.Daily = count
		case "weekly":
			retention.Weekly = count
		case "monthly":
			retention.Monthly = count
		case "yearly":
			retention.Yearly = count
		default:
			return retention, formProblem("retention: unknown key %q (use last, daily, weekly, monthly, yearly)", key)
		}
	}
	return retention, nil
}

// job turns the form into a job, or says what is wrong with an input.
func (f jobForm) job() (configedit.Job, error) {
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

		PasswordLiteral: f.passwordKept,
	}
	if portText := strings.TrimSpace(f.field("port").value); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil {
			return job, formProblem("port: %q is not a number", portText)
		}
		job.Port = port
	}
	job.Repositories = strings.Fields(strings.ReplaceAll(f.field("repositories").value, ",", " "))
	retention, err := parseRetention(f.field("retention").value)
	if err != nil {
		return job, err
	}
	job.Retention = retention
	return job, nil
}

func (d manager) openForm(job configedit.Job, original string, repositories []string) manager {
	d.form = newJobForm(job, original, repositories)
	d.mode = modeForm
	return d
}

func (d manager) onFormKey(key, text string) (tea.Model, tea.Cmd) {
	shown := d.form.visible()
	position := 0
	for i, index := range shown {
		if index == d.form.focus {
			position = i
		}
	}
	move := func(delta int) {
		d.form.focus = shown[(position+delta+len(shown))%len(shown)]
	}
	focused := &d.form.fields[d.form.focus]
	switch key {
	case "esc":
		d.mode = modeTable
	case "tab", "down":
		move(1)
	case "shift+tab", "up":
		move(-1)
	case "ctrl+s":
		return d.saveForm()
	case "enter":
		if position == len(shown)-1 {
			return d.saveForm()
		}
		move(1)
	case "left", "right", " ":
		if focused.kind == fieldChoice && (focused.key != "type" || d.form.original == "") {
			focused.value = cycle(focused.choices, focused.value, key == "left")
		} else if focused.kind == fieldText && key == " " {
			focused.value += " "
		}
	default:
		if focused.kind == fieldText {
			focused.value = editText(focused.value, key, text)
		}
	}
	d.form.problem = ""
	return d, nil
}

func cycle(choices []string, current string, backwards bool) string {
	index := 0
	for i, choice := range choices {
		if choice == current {
			index = i
		}
	}
	step := 1
	if backwards {
		step = len(choices) - 1
	}
	return choices[(index+step)%len(choices)]
}

func (d manager) saveForm() (tea.Model, tea.Cmd) {
	job, err := d.form.job()
	if err == nil {
		err = d.act.saveJob(d.form.original, job)
	}
	var invalid *configedit.InvalidError
	switch {
	case err == nil:
		verb := "Added"
		if d.form.original != "" {
			verb = "Saved"
		}
		d.mode, d.notice, d.noticeBad = modeTable, fmt.Sprintf("%s job %s in the config. Restart the daemon (sentinel run) to apply it.", verb, job.Name), false
		d = d.refreshPairs()
		return d, d.reload()
	case errors.As(err, &invalid), errors.Is(err, configedit.ErrExists), errors.Is(err, configedit.ErrNotFound):
		d.form.problem = err.Error()
	default:
		// Not the user's input: leave the form so the failure is not mistaken for a validation message.
		d.mode, d.notice, d.noticeBad = modeTable, "Could not save the config: "+err.Error(), true
	}
	return d, nil
}

// refreshPairs re-reads the config so new jobs show up (and removed ones go).
func (d manager) refreshPairs() manager {
	if listing, err := d.act.listJobs(); err == nil {
		d.pairs = pairsOf(listing)
	}
	return d
}

func pairsOf(listing configedit.Listing) []jobRepository {
	var pairs []jobRepository
	for _, job := range listing.Jobs {
		for _, repository := range job.Repositories {
			pairs = append(pairs, jobRepository{job: job.Name, repository: repository})
		}
	}
	return pairs
}

// startAdd opens an empty form.
func (d manager) startAdd() manager {
	listing, err := d.act.listJobs()
	if err != nil {
		d.notice, d.noticeBad = "Could not read the config: "+err.Error(), true
		return d
	}
	return d.openForm(configedit.Job{Type: config.BackupPostgres}, "", listing.Repositories)
}

// startEdit opens the form for the selected job, or says why it cannot.
func (d manager) startEdit() manager {
	line, ok := d.selectedBackup()
	if !ok {
		return d
	}
	listing, err := d.act.listJobs()
	if err != nil {
		d.notice, d.noticeBad = "Could not read the config: "+err.Error(), true
		return d
	}
	for _, job := range listing.Jobs {
		if job.Name != line.job {
			continue
		}
		if !job.Editable() {
			d.notice, d.noticeBad = fmt.Sprintf("%s is a %s job; only database jobs can be edited here. Edit the config file.", job.Name, job.Type), true
			return d
		}
		return d.openForm(job, job.Name, listing.Repositories)
	}
	d.notice, d.noticeBad = line.job+" is not in the config file any more.", true
	return d
}

// startDelete asks for the job name before removing a database job.
func (d manager) startDelete() manager {
	line, ok := d.selectedBackup()
	if !ok {
		return d
	}
	listing, err := d.act.listJobs()
	if err != nil {
		d.notice, d.noticeBad = "Could not read the config: "+err.Error(), true
		return d
	}
	for _, job := range listing.Jobs {
		if job.Name == line.job && job.Editable() {
			d.mode, d.deleteName, d.confirm, d.inputError = modeDelete, job.Name, "", ""
			return d
		}
	}
	d.notice, d.noticeBad = line.job+" is not a database job in the config; only those can be removed here.", true
	return d
}

func (d manager) onDeleteKey(key, text string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		d.mode = modeTable
	case "enter":
		if d.confirm != d.deleteName {
			d.inputError = "the name does not match"
			return d, nil
		}
		if err := d.act.deleteJob(d.deleteName); err != nil {
			d.mode, d.notice, d.noticeBad = modeTable, "Could not remove the job: "+err.Error(), true
			return d, nil
		}
		d.mode, d.notice, d.noticeBad = modeTable, fmt.Sprintf("Removed job %s from the config. Its snapshots stay in the repositories. Restart the daemon (sentinel run) to apply it.", d.deleteName), false
		d = d.refreshPairs()
		d.cursor = 0
		return d, d.reload()
	default:
		d.confirm, d.inputError = editText(d.confirm, key, text), ""
	}
	return d, nil
}

func (d manager) renderForm() []string {
	title := "Add a database job"
	if d.form.original != "" {
		title = "Edit " + d.form.original
	}
	out := []string{titleStyle.Render(title), ""}
	for _, index := range d.form.visible() {
		field := d.form.fields[index]
		marker := "  "
		if index == d.form.focus {
			marker = lipglossAccent("› ")
		}
		value := field.value
		if field.kind == fieldChoice {
			value = "‹ " + value + " ›"
		} else if index == d.form.focus {
			value += lipglossAccent("▏")
		}
		out = append(out, fmt.Sprintf("%s%-17s %s", marker, field.label, value))
	}
	out = append(out, "", dimStyle.Render(d.form.fields[d.form.focus].hint))
	if d.form.problem != "" {
		out = append(out, "", severityStyle(sevBad).Render(wrap(d.form.problem, d.width-2)))
	}
	return append(out, "", dimStyle.Render("tab/↑↓ move · enter next, save on the last · ctrl+s save · esc cancel"))
}

func (d manager) renderDelete() []string {
	out := []string{
		titleStyle.Render("Remove " + d.deleteName),
		"",
		wrap("This removes the job from the config file. Existing snapshots stay in the repositories.", d.width-2),
		"",
		"Type the job name (" + d.deleteName + ") to confirm:",
		"  " + d.confirm + lipglossAccent("▏"),
	}
	if d.inputError != "" {
		out = append(out, "", severityStyle(sevBad).Render(d.inputError))
	}
	return append(out, "", dimStyle.Render("enter remove · esc cancel"))
}
