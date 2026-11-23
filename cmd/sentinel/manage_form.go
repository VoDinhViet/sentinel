package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
	"sentinel/internal/schedule"
)

type fieldKind int

const (
	fieldText      fieldKind = iota
	fieldNumber              // digits only
	fieldChoice              // one of choices, with ‹ ›
	fieldChecklist           // several of choices, each ticked or not
	fieldSchedule            // text with presets on ← →, and a plain-words preview
	fieldRetention           // five counts on one line
)

var retentionKeys = [5]string{"last", "daily", "weekly", "monthly", "yearly"}

// schedulePresets are what ← → cycles through on a schedule field.
var schedulePresets = []string{"daily 02:00", "daily 03:00", "hourly", "weekly sun 03:00", "monthly 1 04:00", "every 6h", "manual"}

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
}

// formDefaults are the config's defaults, shown on the fields they fill in.
type formDefaults struct {
	schedule     string
	repositories []string
	retention    config.Retention
}

// jobForm edits one job: a database dump or a set of files. original is the job's name when editing, "" when adding.
type jobForm struct {
	original     string
	passwordKept bool // the file holds a literal password that stays unless an env var is entered
	known        configedit.Known
	defaults     formDefaults
	fields       []formField
	focus        int
	touched      map[string]bool // fields the user has been in: only those show their problem
	attempted    bool            // a save was tried: every problem shows
	problem      string          // a problem of the whole save, not of one field
}

func newJobForm(job configedit.Job, original string, listing configedit.Listing) jobForm {
	form := jobForm{
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
	scheduleHint := `daily 02:00 · weekly sun 03:00 · monthly 1 04:00 · hourly :15 · every 6h · a cron expression · manual; ← → for presets`
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
	form.fields = []formField{
		{key: "type", label: "Type", hint: typeHint, kind: fieldChoice, value: job.Type, choices: configedit.JobTypes, fixed: original != ""},
		{key: "name", label: "Name", hint: "letters, digits, . _ -", value: job.Name},
		{key: "paths", label: "Paths", hint: "files and folders to back up, separated by commas; ${HOME} works", value: strings.Join(job.Paths, ", ")},
		{key: "exclude", label: "Exclude", hint: "patterns to skip, separated by commas, e.g. *.tmp, node_modules", value: strings.Join(job.Exclude, ", ")},
		{key: "database", label: "Database", hint: "the database to dump", value: job.Database},
		{key: "host", label: "Host", hint: "default localhost", value: job.Host, placeholder: "localhost"},
		{key: "port", label: "Port", hint: "default 5432 / 3306 / 27017", kind: fieldNumber, value: port, placeholder: "default"},
		{key: "user", label: "User", hint: "optional", value: job.User},
		{key: "password_env", label: "Password env var", hint: passwordHint, value: job.PasswordEnv},
		{key: "schedule", label: "Schedule", hint: scheduleHint, kind: fieldSchedule, value: job.Schedule, placeholder: scheduleNote},
		{key: "repositories", label: "Repositories", hint: repositoriesHint, kind: fieldChecklist, choices: listing.Repositories, ticked: ticked},
		{key: "retention", label: "Keep", hint: "how many snapshots to keep: last N, then one per day, week, month, year; all empty = keep everything", kind: fieldRetention, counts: retention},
		{key: "tls", label: "TLS", hint: "mysql only: empty, disabled or required", value: job.TLS},
		{key: "auth_database", label: "Auth database", hint: "mongodb only: where the user is defined", value: job.AuthDatabase},
	}
	return form
}

// fieldsOf are the field keys that apply to one job type only; the others apply to all.
var fieldsOf = map[string][]string{
	"paths":         {config.BackupFiles},
	"exclude":       {config.BackupFiles},
	"database":      configedit.DatabaseTypes,
	"host":          configedit.DatabaseTypes,
	"port":          configedit.DatabaseTypes,
	"user":          configedit.DatabaseTypes,
	"password_env":  configedit.DatabaseTypes,
	"tls":           {config.BackupMySQL},
	"auth_database": {config.BackupMongoDB},
}

// visible are the fields that apply to the chosen type.
func (f jobForm) visible() []int {
	jobType := f.field("type").value
	var shown []int
	for i, field := range f.fields {
		if types, restricted := fieldsOf[field.key]; restricted && !slices.Contains(types, jobType) {
			continue
		}
		shown = append(shown, i)
	}
	return shown
}

// splitList splits a comma separated list, dropping blanks.
func splitList(text string) []string {
	var items []string
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func (f jobForm) field(key string) *formField {
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
func (f jobForm) problems() map[string]string {
	job, err := f.job()
	found := map[string]string{}
	var invalid *configedit.InvalidError
	if errors.As(err, &invalid) {
		found[invalid.Field] = invalid.Message
	}
	for _, problem := range configedit.Problems(&job, f.known) {
		if _, seen := found[problem.Field]; !seen {
			found[problem.Field] = problem.Message
		}
	}
	return found
}

// shownProblems are the problems of the fields the user has been in (all after a failed save).
func (f jobForm) shownProblems() map[string]string {
	shown := map[string]string{}
	for field, message := range f.problems() {
		if f.attempted || f.touched[field] {
			shown[field] = message
		}
	}
	return shown
}

func (m manageModel) openForm(job configedit.Job, original string, listing configedit.Listing) manageModel {
	m.form = newJobForm(job, original, listing)
	m.mode = modeForm
	return m
}

// focusOn moves the focus, remembering that the field it leaves has been visited.
func (f *jobForm) focusOn(index int) {
	f.touched[f.fields[f.focus].key] = true
	f.focus = index
}

func (m manageModel) onFormKey(key, text string) (tea.Model, tea.Cmd) {
	shown := m.form.visible()
	position := 0
	for i, index := range shown {
		if index == m.form.focus {
			position = i
		}
	}
	move := func(delta int) { m.form.focusOn(shown[(position+delta+len(shown))%len(shown)]) }
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
	case "left", "right":
		m.form.step(m.form.focus, map[string]int{"left": -1, "right": 1}[key])
	case " ":
		switch focused.kind {
		case fieldChecklist:
			m.form.toggle(m.form.focus, focused.sub)
		case fieldText, fieldSchedule:
			focused.value += " "
		}
	default:
		m.form.typeInto(focused, key, text)
	}
	m.form.touched[focused.key] = true
	m.form.problem = ""
	return m, nil
}

// typeInto applies a key press to the field's text.
func (f *jobForm) typeInto(field *formField, key, text string) {
	digits := text != "" && strings.Trim(text, "0123456789") == ""
	switch field.kind {
	case fieldText, fieldSchedule:
		field.value = editText(field.value, key, text)
	case fieldNumber:
		if key == "backspace" || digits {
			field.value = editText(field.value, key, text)
		}
	case fieldRetention:
		if (key == "backspace" || digits) && len([]rune(field.counts[field.sub])) < 4 || key == "backspace" {
			field.counts[field.sub] = editText(field.counts[field.sub], key, text)
		}
	}
}

// step is ← or →: the previous or next choice, preset, tick position or count.
func (f *jobForm) step(index, delta int) {
	field := &f.fields[index]
	switch field.kind {
	case fieldChoice:
		if !field.fixed {
			field.value = cycle(field.choices, field.value, delta < 0)
		}
	case fieldSchedule:
		field.value = cycle(schedulePresets, field.value, delta < 0)
	case fieldChecklist:
		field.sub = min(max(field.sub+delta, 0), max(len(field.choices)-1, 0))
	case fieldRetention:
		field.sub = min(max(field.sub+delta, 0), len(retentionKeys)-1)
	}
	f.touched[field.key] = true
}

func (f *jobForm) toggle(index, item int) {
	field := &f.fields[index]
	if field.kind == fieldChecklist && item >= 0 && item < len(field.ticked) {
		field.ticked[item] = !field.ticked[item]
		field.sub = item
	}
	f.touched[field.key] = true
}

func cycle(choices []string, current string, backwards bool) string {
	index := -1
	for i, choice := range choices {
		if choice == current {
			index = i
		}
	}
	if index < 0 { // not one of them (a typed schedule): start from the first or last
		if backwards {
			return choices[len(choices)-1]
		}
		return choices[0]
	}
	step := 1
	if backwards {
		step = len(choices) - 1
	}
	return choices[(index+step)%len(choices)]
}

// formAction handles the clicks that are specific to forms.
func (m manageModel) formAction(action string) (manageModel, bool) {
	parts := strings.Split(action, ":")
	numbers := make([]int, 0, 2)
	for _, part := range parts[1:] {
		number, err := strconv.Atoi(part)
		if err != nil {
			return m, false
		}
		numbers = append(numbers, number)
	}
	if len(numbers) == 0 || numbers[0] < 0 || numbers[0] >= len(m.form.fields) {
		return m, false
	}
	index := numbers[0]
	switch {
	case parts[0] == "focus" && len(numbers) <= 2:
		m.form.focusOn(index)
		if len(numbers) == 2 {
			m.form.fields[index].sub = numbers[1]
		}
	case parts[0] == "toggle" && len(numbers) == 2:
		m.form.focusOn(index)
		m.form.toggle(index, numbers[1])
	case parts[0] == "step" && len(numbers) == 2:
		m.form.focusOn(index)
		m.form.step(index, numbers[1])
	default:
		return m, false
	}
	m.form.problem = ""
	return m, true
}

func (m manageModel) saveForm() (tea.Model, tea.Cmd) {
	m.form.attempted = true
	job, err := m.form.job()
	if err == nil {
		err = configedit.Validate(&job, m.form.known)
	}
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
		if errors.As(err, &invalid) && invalid.Field != "" {
			for i, field := range m.form.fields {
				if field.key == invalid.Field {
					m.form.focusOn(i)
					break
				}
			}
		} else {
			m.form.problem = err.Error()
		}
	default:
		// Not the user's input: leave the form so the failure is not mistaken for a validation message.
		m.mode, m.notice, m.noticeBad = modeTable, "Could not save the config: "+err.Error(), true
	}
	return m, nil
}

const formLabelWidth = 17

// inputStyle paints the focused input with the selection background.
func inputStyle(focused bool, text string, width int) string {
	if !focused {
		return text + strings.Repeat(" ", max(width-lipgloss.Width(text), 0))
	}
	return selectedStyle.Render(text + strings.Repeat(" ", max(width-lipgloss.Width(text), 0)))
}

func (m manageModel) renderForm() []string {
	title := "Add a job"
	if m.form.original != "" {
		title = "Edit " + m.form.original
	}
	screenLines := []string{titleStyle.Render(title), ""}
	problems := m.form.shownProblems()
	valid := len(m.form.problems()) == 0
	valueColumn := 2 + formLabelWidth + 1
	for _, index := range m.form.visible() {
		field := m.form.fields[index]
		focused := index == m.form.focus
		marker := "  "
		if focused {
			marker = lipglossAccent("› ")
		}
		label := fmt.Sprintf("%-*s", formLabelWidth, field.label)
		if _, bad := problems[field.key]; bad {
			label = severityStyle(sevBad).Render(label)
		}
		row := len(screenLines)
		var value string
		switch field.kind {
		case fieldChoice:
			value = inputStyle(focused, "‹ "+field.value+" ›", 0)
			if !field.fixed {
				m.hits.add(row, valueColumn, 1, fmt.Sprintf("step:%d:-1", index))
				m.hits.add(row, valueColumn+lipgloss.Width(field.value)+3, 1, fmt.Sprintf("step:%d:1", index))
			}
			m.hits.add(row, 0, valueColumn, fmt.Sprintf("focus:%d", index))
		case fieldChecklist:
			column := valueColumn
			var items []string
			for i, choice := range field.choices {
				box := "[ ] " + choice
				if field.ticked[i] {
					box = "[x] " + choice
				}
				items = append(items, inputStyle(focused && field.sub == i, box, 0))
				m.hits.add(row, column, lipgloss.Width(box), fmt.Sprintf("toggle:%d:%d", index, i))
				column += lipgloss.Width(box) + 2
			}
			value = strings.Join(items, "  ")
			if len(field.choices) == 0 {
				value = dimStyle.Render("no repository in the config yet")
			} else if inherited := m.form.defaults.repositories; len(inherited) > 0 && !slicesAny(field.ticked) {
				value += dimStyle.Render("   default: " + strings.Join(inherited, ", "))
			}
			m.hits.add(row, 0, valueColumn, fmt.Sprintf("focus:%d", index))
		case fieldRetention:
			column := valueColumn
			var items []string
			for i, name := range retentionKeys {
				current := focused && field.sub == i
				box := inputStyle(current, fmt.Sprintf("%4s", field.counts[i]), 4)
				if field.counts[i] == "" && !current {
					box = dimStyle.Render(fmt.Sprintf("%4s", m.retentionPlaceholder(i)))
				}
				items = append(items, dimStyle.Render(name+" ")+box)
				width := lipgloss.Width(name) + 1 + 4
				m.hits.add(row, column, width, fmt.Sprintf("focus:%d:%d", index, i))
				column += width + 2
			}
			value = strings.Join(items, "  ")
			m.hits.add(row, 0, valueColumn, fmt.Sprintf("focus:%d", index))
		default: // text, number, schedule
			value = field.value
			if focused {
				value += lipglossAccent("▏")
			}
			if field.value == "" && field.placeholder != "" {
				value += dimStyle.Render(field.placeholder)
			}
			m.hits.add(row, 0, m.width, fmt.Sprintf("focus:%d", index))
		}
		screenLines = append(screenLines, marker+label+" "+value)
		if message, bad := problems[field.key]; bad {
			screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+severityStyle(sevBad).Render("✗ "+truncate(message, max(m.width-formLabelWidth-6, 10))))
		} else if field.key == "paths" {
			if note := pathsNote(field.value); note != "" {
				screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+note)
			}
		} else if field.kind == fieldSchedule {
			if preview := m.schedulePreview(field.value); preview != "" {
				screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+dimStyle.Render(preview))
			}
		}
	}
	screenLines = append(screenLines, "", dimStyle.Render(wrap(m.form.fields[m.form.focus].hint, m.width-2)))
	if m.form.problem != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(wrap(m.form.problem, m.width-2)))
	}
	saveButton := "[ Save ]"
	saveStyle := keyStyle
	if !valid {
		saveStyle = dimStyle
	}
	buttons := saveStyle.Render(saveButton) + "  " + keyStyle.Render("[ Cancel ]")
	buttonRow := len(screenLines) + 1
	m.hits.add(buttonRow, 0, lipgloss.Width(saveButton), "ctrl+s")
	m.hits.add(buttonRow, lipgloss.Width(saveButton)+2, lipgloss.Width("[ Cancel ]"), "esc")
	screenLines = append(screenLines, "", buttons)
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "tab", "next", "space", "tick", "ctrl+s", "save", "esc", "cancel"))
}

// pathsNote says whether the listed paths exist on this machine.
func pathsNote(list string) string {
	var missing []string
	paths := splitList(list)
	for _, path := range paths {
		if _, err := os.Stat(os.ExpandEnv(path)); err != nil {
			missing = append(missing, path)
		}
	}
	switch {
	case len(paths) == 0:
		return ""
	case len(missing) == 0:
		return severityStyle(sevOK).Render(fmt.Sprintf("✓ found on this machine (%d)", len(paths)))
	}
	return severityStyle(sevWarn).Render("▲ not found here: " + strings.Join(missing, ", "))
}

func slicesAny(flags []bool) bool {
	for _, flag := range flags {
		if flag {
			return true
		}
	}
	return false
}

// retentionPlaceholder is the default count shown on an empty retention input.
func (m manageModel) retentionPlaceholder(part int) string {
	retention := m.form.defaults.retention
	count := []int{retention.Last, retention.Daily, retention.Weekly, retention.Monthly, retention.Yearly}[part]
	if count == 0 {
		return ""
	}
	return strconv.Itoa(count)
}

// schedulePreview says in words when the schedule runs next, or why it does not.
func (m manageModel) schedulePreview(spec string) string {
	if strings.TrimSpace(spec) == "" {
		spec = m.form.defaults.schedule
	}
	if schedule.IsManual(spec) {
		return "→ only run by hand"
	}
	next, ok := schedule.Next(spec, time.Now())
	if !ok {
		return ""
	}
	return fmt.Sprintf("→ %s · next %s (%s)", schedule.Describe(spec), next.Local().Format("Mon 02 Jan 15:04"), inFuture(time.Until(next)))
}

// inFuture is a rough "in 13h" for a duration ahead.
func inFuture(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "in under a minute"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	}
	return fmt.Sprintf("in %dd", int(d.Hours()/24))
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
		repositories := job.Repositories
		if job.Inherited.Repositories {
			repositories = listing.InheritedRepositories
		}
		for _, repository := range repositories {
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
	return m.openForm(configedit.Job{Type: config.BackupPostgres}, "", listing)
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
			m.notice, m.noticeBad = fmt.Sprintf("%s is a %s job, which this screen cannot edit. Edit the config file.", job.Name, job.Type), true
			return m
		}
		return m.openForm(job, job.Name, listing)
	}
	m.notice, m.noticeBad = line.job+" is not in the config file any more.", true
	return m
}

// startDelete asks for the job name before removing a job.
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
	m.notice, m.noticeBad = line.job+" is not a job this screen can remove (it is not in the config file, or has another type).", true
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
