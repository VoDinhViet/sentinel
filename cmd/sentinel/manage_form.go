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
		{key: "schedule", label: "Schedule", hint: `daily 02:00 · weekly sun 03:00 · monthly 1 04:00 · every 6h · hourly :15 · or cron "0 3 * * *"; empty = only by hand`, value: job.Schedule},
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
	var cellTexts []string
	for _, field := range []struct {
		key   string
		value int
	}{{"last", r.Last}, {"daily", r.Daily}, {"weekly", r.Weekly}, {"monthly", r.Monthly}, {"yearly", r.Yearly}} {
		if field.value > 0 {
			cellTexts = append(cellTexts, fmt.Sprintf("%s=%d", field.key, field.value))
		}
	}
	return strings.Join(cellTexts, " ")
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

func (m manageModel) openForm(job configedit.Job, original string, repositories []string) manageModel {
	m.form = newJobForm(job, original, repositories)
	m.mode = modeForm
	return m
}

func (m manageModel) onFormKey(key, text string) (tea.Model, tea.Cmd) {
	shown := m.form.visible()
	position := 0
	for i, index := range shown {
		if index == m.form.focus {
			position = i
		}
	}
	move := func(delta int) {
		m.form.focus = shown[(position+delta+len(shown))%len(shown)]
	}
	focused := &m.form.fields[m.form.focus]
	switch key {
	case "esc":
		m.mode = modeTable
	case "tab", "down":
		move(1)
	case "shift+tab", "up":
		move(-1)
	case "ctrl+s":
		return m.saveForm()
	case "enter":
		if position == len(shown)-1 {
			return m.saveForm()
		}
		move(1)
	case "left", "right", " ":
		if focused.kind == fieldChoice && (focused.key != "type" || m.form.original == "") {
			focused.value = cycle(focused.choices, focused.value, key == "left")
		} else if focused.kind == fieldText && key == " " {
			focused.value += " "
		}
	default:
		if focused.kind == fieldText {
			focused.value = editText(focused.value, key, text)
		}
	}
	m.form.problem = ""
	return m, nil
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

func (m manageModel) saveForm() (tea.Model, tea.Cmd) {
	job, err := m.form.job()
	if err == nil {
		err = m.actions.saveJob(m.form.original, job)
	}
	var invalid *configedit.InvalidError
	switch {
	case err == nil:
		verb := "Added"
		if m.form.original != "" {
			verb = "Saved"
		}
		m.mode, m.notice, m.noticeBad = modeTable, fmt.Sprintf("%s job %s in the config. Restart the daemon (sentinel run) to apply it.", verb, job.Name), false
		m = m.refreshJobRepositories()
		return m, m.reload()
	case errors.As(err, &invalid), errors.Is(err, configedit.ErrExists), errors.Is(err, configedit.ErrNotFound):
		m.form.problem = err.Error()
	default:
		// Not the user's input: leave the form so the failure is not mistaken for a validation message.
		m.mode, m.notice, m.noticeBad = modeTable, "Could not save the config: "+err.Error(), true
	}
	return m, nil
}

// refreshPairs re-reads the config so new jobs show up (and removed ones go).
func (m manageModel) refreshJobRepositories() manageModel {
	if listing, err := m.actions.listJobs(); err == nil {
		m.jobRepositories = jobRepositoriesOf(listing)
	}
	return m
}

func jobRepositoriesOf(listing configedit.Listing) []jobRepository {
	var jobRepositories []jobRepository
	for _, job := range listing.Jobs {
		for _, repository := range job.Repositories {
			jobRepositories = append(jobRepositories, jobRepository{job: job.Name, repository: repository})
		}
	}
	return jobRepositories
}

// startAdd opens an empty form.
func (m manageModel) startAdd() manageModel {
	listing, err := m.actions.listJobs()
	if err != nil {
		m.notice, m.noticeBad = "Could not read the config: "+err.Error(), true
		return m
	}
	return m.openForm(configedit.Job{Type: config.BackupPostgres}, "", listing.Repositories)
}

// startEdit opens the form for the selected job, or says why it cannot.
func (m manageModel) startEdit() manageModel {
	line, ok := m.selectedBackup()
	if !ok {
		return m
	}
	listing, err := m.actions.listJobs()
	if err != nil {
		m.notice, m.noticeBad = "Could not read the config: "+err.Error(), true
		return m
	}
	for _, job := range listing.Jobs {
		if job.Name != line.job {
			continue
		}
		if !job.Editable() {
			m.notice, m.noticeBad = fmt.Sprintf("%s is a %s job; only database jobs can be edited here. Edit the config file.", job.Name, job.Type), true
			return m
		}
		return m.openForm(job, job.Name, listing.Repositories)
	}
	m.notice, m.noticeBad = line.job+" is not in the config file any more.", true
	return m
}

// startDelete asks for the job name before removing a database job.
func (m manageModel) startDelete() manageModel {
	line, ok := m.selectedBackup()
	if !ok {
		return m
	}
	listing, err := m.actions.listJobs()
	if err != nil {
		m.notice, m.noticeBad = "Could not read the config: "+err.Error(), true
		return m
	}
	for _, job := range listing.Jobs {
		if job.Name == line.job && job.Editable() {
			m.mode, m.deleteName, m.confirm, m.inputError = modeDelete, job.Name, "", ""
			return m
		}
	}
	m.notice, m.noticeBad = line.job+" is not a database job in the config; only those can be removed here.", true
	return m
}

func (m manageModel) onDeleteKey(key, text string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = modeTable
	case "enter":
		if m.confirm != m.deleteName {
			m.inputError = "the name does not match"
			return m, nil
		}
		if err := m.actions.deleteJob(m.deleteName); err != nil {
			m.mode, m.notice, m.noticeBad = modeTable, "Could not remove the job: "+err.Error(), true
			return m, nil
		}
		m.mode, m.notice, m.noticeBad = modeTable, fmt.Sprintf("Removed job %s from the config. Its snapshots stay in the repositories. Restart the daemon (sentinel run) to apply it.", m.deleteName), false
		m = m.refreshJobRepositories()
		m.cursor = 0
		return m, m.reload()
	default:
		m.confirm, m.inputError = editText(m.confirm, key, text), ""
	}
	return m, nil
}

func (m manageModel) renderForm() []string {
	title := "Add a database job"
	if m.form.original != "" {
		title = "Edit " + m.form.original
	}
	screenLines := []string{titleStyle.Render(title), ""}
	for _, index := range m.form.visible() {
		field := m.form.fields[index]
		marker := "  "
		if index == m.form.focus {
			marker = lipglossAccent("› ")
		}
		value := field.value
		if field.kind == fieldChoice {
			value = "‹ " + value + " ›"
		} else if index == m.form.focus {
			value += lipglossAccent("▏")
		}
		screenLines = append(screenLines, fmt.Sprintf("%s%-17s %s", marker, field.label, value))
	}
	screenLines = append(screenLines, "", dimStyle.Render(m.form.fields[m.form.focus].hint))
	if m.form.problem != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(wrap(m.form.problem, m.width-2)))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "tab", "next", "enter", "next/save", "ctrl+s", "save", "esc", "cancel"))
}

func (m manageModel) renderDelete() []string {
	screenLines := []string{
		titleStyle.Render("Remove " + m.deleteName),
		"",
		wrap("This removes the job from the config file. Existing snapshots stay in the repositories.", m.width-2),
		"",
		"Type the job name (" + m.deleteName + ") to confirm:",
		"  " + m.confirm + lipglossAccent("▏"),
	}
	if m.inputError != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(m.inputError))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "enter", "remove", "esc", "cancel"))
}
