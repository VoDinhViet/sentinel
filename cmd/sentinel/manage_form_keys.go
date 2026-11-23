package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/configedit"
	"sentinel/internal/schedule"
)

func (m manageModel) openForm(job configedit.Job, original string, listing configedit.Listing) manageModel {
	return m.openEntryForm(newJobForm(job, original, listing))
}

func (m manageModel) openEntryForm(next form) manageModel {
	m.form = next
	m.mode = modeForm
	return m
}

// focusOn moves the focus, remembering that the field it leaves has been visited.
func (f *form) focusOn(index int) {
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
	if key != "tab" {
		focused.options = nil
	}
	switch key {
	case "esc", "ctrl+c":
		m.mode = modeTable
	case "tab":
		if m.form.completeFocused() {
			return m, nil
		}
		move(1)
	case "down":
		move(1)
	case "shift+tab", "up":
		move(-1)
	case "ctrl+s":
		return m.saveForm()
	case "ctrl+o":
		if focused.kind == fieldPaths {
			next, cmd := m.openBrowse()
			return next, cmd
		}
	case "enter":
		if position == len(shown)-1 {
			return m.saveForm()
		}
		move(1)
	case "alt+up", "alt+down", "alt+left", "alt+right":
		if focused.kind == fieldSchedule {
			timeSteps, dayDelta := map[string]int{"alt+up": 1, "alt+down": -1}[key], map[string]int{"alt+right": 1, "alt+left": -1}[key]
			if adjusted, ok := schedule.Adjust(focused.value, timeSteps, dayDelta); ok {
				focused.value = adjusted
			}
		}
	case "left", "right":
		m.form.step(m.form.focus, map[string]int{"left": -1, "right": 1}[key])
	case " ":
		switch focused.kind {
		case fieldChecklist:
			m.form.toggle(m.form.focus, focused.sub)
		case fieldText, fieldSchedule, fieldPaths:
			focused.value += " "
		}
	default:
		if number, ok := strings.CutPrefix(key, "alt+"); ok && focused.kind == fieldPaths {
			m.form.suggest(m.form.focus, number)
			break
		}
		m.form.typeInto(focused, key, text)
	}
	m.form.touched[focused.key] = true
	m.form.problem = ""
	return m, nil
}

// typeInto applies a key press to the field's text.
func (f *form) typeInto(field *formField, key, text string) {
	if field.fixed {
		return
	}
	digits := text != "" && strings.Trim(text, "0123456789") == ""
	switch field.kind {
	case fieldText, fieldSchedule, fieldPaths:
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

// suggest adds the suggestion with that number (1 to 9) to a Paths field.
func (f *form) suggest(index int, number string) {
	field := &f.fields[index]
	choice, err := strconv.Atoi(number)
	suggestions := suggestFolders(splitList(field.value))
	if err != nil || choice < 1 || choice > len(suggestions) {
		return
	}
	field.value = addPath(field.value, suggestions[choice-1])
	f.touched[field.key] = true
}

// step is ← or →: the previous or next choice, preset, tick position or count.
func (f *form) step(index, delta int) {
	field := &f.fields[index]
	switch field.kind {
	case fieldChoice:
		if !field.fixed {
			field.value = cycle(field.choices, field.value, delta < 0)
		}
	case fieldSchedule:
		field.value = schedule.NextFrequency(field.value, delta < 0)
	case fieldChecklist:
		field.sub = min(max(field.sub+delta, 0), max(len(field.choices)-1, 0))
	case fieldRetention:
		field.sub = min(max(field.sub+delta, 0), len(retentionKeys)-1)
	}
	f.touched[field.key] = true
}

func (f *form) toggle(index, sub int) {
	field := &f.fields[index]
	if field.kind == fieldChecklist && sub >= 0 && sub < len(field.ticked) {
		field.ticked[sub] = !field.ticked[sub]
		field.sub = sub
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
	case parts[0] == "suggest" && len(numbers) == 2:
		m.form.focusOn(index)
		m.form.suggest(index, strconv.Itoa(numbers[1]))
	default:
		return m, false
	}
	m.form.problem = ""
	return m, true
}

func (m manageModel) saveForm() (tea.Model, tea.Cmd) {
	m.form.attempted = true
	label, err := m.form.apply(m.actions)
	var invalid *configedit.InvalidError
	switch {
	case err == nil:
		verb := "Added"
		if m.form.original != "" {
			verb = "Saved"
		}
		m.mode, m.notice, m.noticeBad = modeTable, fmt.Sprintf("%s %s in the config. Restart the daemon (sentinel run) to apply it.", verb, label), false
		m = m.refreshJobRepositories().refreshConfig()
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

// apply saves what the form holds and says what was saved ("job shop"). The error is an
// InvalidError (with the field) when the input is at fault.
func (f form) apply(actions manageActions) (string, error) {
	if f.kind != formJob {
		return f.applyEntry(actions)
	}
	job, known, repo, err := f.jobWithStore()
	if err == nil {
		err = configedit.Validate(&job, known)
	}
	if err != nil {
		return "job " + job.Name, err
	}
	label := "job " + job.Name
	if repo.URL != "" {
		repo.PasswordEnv = f.sharedPasswordEnv(actions)
		if err := actions.saveRepository("", repo); err != nil {
			return label, err
		}
		label += " with its backups in " + repo.URL + " (create the repository once with: sentinel backup init " + job.Name + ")"
	}
	if err := actions.saveJob(f.original, job); err != nil {
		if repo.URL != "" {
			err = fmt.Errorf("repository %s was created, but the job was not saved: %w", repo.Name, err)
		}
		return label, err
	}
	return label, nil
}

// sharedPasswordEnv is the password variable of the first repository that has one, so a new
// repository keeps using the passphrase the user already has; RESTIC_PASSWORD when there is none.
func (f form) sharedPasswordEnv(actions manageActions) string {
	if actions.overview != nil {
		if overview, err := actions.overview(); err == nil {
			for _, repo := range overview.Repositories {
				if repo.PasswordEnv != "" {
					return repo.PasswordEnv
				}
			}
		}
	}
	return "RESTIC_PASSWORD"
}
