package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/configedit"
	"sentinel/internal/schedule"
)

func (m manageModel) openForm(job configedit.Job, original string, listing configedit.Listing) manageModel {
	return m.openEntryForm(newJobForm(job, original, listing))
}

func (m manageModel) openEntryForm(next form) manageModel {
	m.form = next
	m.form.disk = &diskMemo{}
	return m.push(formScreen{})
}

// formScreen is the form that adds or edits a config entry. Its fields are in m.form, not in the
// screen: the init program and the save and test actions work on the form wherever it is open.
type formScreen struct{}

func (formScreen) onKey(m manageModel, key, text string) (manageModel, tea.Cmd) {
	next, cmd := m.onFormKey(key, text)
	return next.(manageModel), cmd
}

func (formScreen) render(m manageModel) []string { return m.renderForm() }

// formOpen says the form is the screen on show, not the folder browser over it.
func (m manageModel) formOpen() bool {
	_, ok := m.top().(formScreen)
	return ok
}

// focusOn moves the focus, remembering that the field it leaves has been visited.
func (f *form) focusOn(index int) {
	f.touched[f.fields[f.focus].key] = true
	f.fields[f.focus].back = 0
	f.focus = index
	f.disk.forget()
}

func (m manageModel) onFormKey(key, text string) (tea.Model, tea.Cmd) {
	before := m.form.connectionKey()
	next, cmd := m.formKey(key, text)
	if after, ok := next.(manageModel); ok && after.formOpen() && after.form.connectionKey() != before {
		after.form.connect = connectState{} // what the test found is about other values
		return after, cmd
	}
	return next, cmd
}

// startConnect tests the connection of the database job in the form, in the background.
func (m manageModel) startConnect() (tea.Model, tea.Cmd) {
	if m.form.databaseType() == "" || m.actions.testConnection == nil || m.form.connect.running {
		return m, nil
	}
	job, err := m.form.connectionJob()
	if err != nil {
		m.form.connect = connectState{text: err.Error(), bad: true}
		return m, nil
	}
	m.connectSeq++
	seq, original, actions, ctx := m.connectSeq, m.form.original, m.actions, m.ctx
	m.form.connect = connectState{seq: seq, running: true, text: "đang thử kết nối…"}
	return m, func() tea.Msg {
		took, err := actions.testConnection(ctx, job, original)
		return connectTestedMsg{seq: seq, took: took, err: err}
	}
}

// afterConnect shows what the connection test found, unless the form has moved on.
func (m manageModel) afterConnect(msg connectTestedMsg) manageModel {
	if !m.formOpen() || m.form.connect.seq != msg.seq {
		return m
	}
	if msg.err != nil {
		m.form.connect = connectState{text: msg.err.Error(), bad: true}
		return m
	}
	m.form.connect = connectState{text: fmt.Sprintf("kết nối được (%s)", msg.took.Round(time.Millisecond))}
	return m
}

func (m manageModel) formKey(key, text string) (tea.Model, tea.Cmd) {
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
		m = m.pop()
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
	case "ctrl+t":
		return m.startConnect()
	case "ctrl+o":
		if focused.kind == fieldPaths {
			next, cmd := m.openBrowse()
			return next, cmd
		}
	case "enter":
		if focused.kind == fieldToggle {
			m.form.advancedOpen = !m.form.advancedOpen
			break
		}
		if position == len(shown)-1 {
			return m.saveForm()
		}
		move(1)
	case "alt+up", "alt+down", "alt+left", "alt+right":
		switch {
		case focused.editsText(): // a word left or right
			focused.edit(keyPress(key, ""))
		case focused.kind == fieldSchedule:
			timeSteps, dayDelta := map[string]int{"alt+up": 1, "alt+down": -1}[key], map[string]int{"alt+right": 1, "alt+left": -1}[key]
			if adjusted, ok := schedule.Adjust(focused.value, timeSteps, dayDelta); ok {
				focused.value = adjusted
			}
		}
	case "left", "right":
		if focused.editsText() {
			focused.edit(keyPress(key, ""))
			break
		}
		m.form.step(m.form.focus, map[string]int{"left": -1, "right": 1}[key])
	case " ", "space": // bubbletea names the space bar "space"; the tests of older code sent " "
		switch focused.kind {
		case fieldToggle:
			m.form.advancedOpen = !m.form.advancedOpen
		case fieldChecklist:
			m.form.toggle(m.form.focus, focused.sub)
		case fieldText, fieldPaths:
			focused.edit(keyPress(" ", " "))
		case fieldSchedule:
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
	m.form.syncSchedules()
	return m, nil
}

// typeInto applies a key press to the field's text.
func (f *form) typeInto(field *formField, key, text string) {
	if field.fixed {
		return
	}
	digits := text != "" && strings.Trim(text, "0123456789") == ""
	switch field.kind {
	case fieldText, fieldPaths, fieldNumber:
		field.edit(keyPress(key, text))
	case fieldSchedule:
		field.value = editText(field.value, key, text)
	case fieldRetention:
		if (key == "backspace" || digits) && len([]rune(field.counts[field.sub])) < 4 || key == "backspace" {
			field.counts[field.sub] = editText(field.counts[field.sub], key, text)
		}
	case fieldTime:
		if digits && validClock(field.value) { // a digit over a whole time starts a new one
			field.value = ""
		}
		if key == "backspace" || ((digits || text == ":") && len([]rune(field.value)) < 5) {
			field.value = editText(field.value, key, text)
			if hour, minute, ok := parseClock(field.value); ok {
				owner := f.field(strings.TrimSuffix(field.key, "_time"))
				if adjusted, changed := schedule.SetTime(owner.value, hour, minute); changed {
					owner.value = adjusted
				}
			}
		}
	}
}

// suggest adds the suggestion with that number (1 to 9) to a Paths field.
func (f *form) suggest(index int, number string) {
	field := &f.fields[index]
	choice, err := strconv.Atoi(number)
	suggestions := f.disk.suggestFolders(splitList(field.value))
	if err != nil || choice < 1 || choice > len(suggestions) {
		return
	}
	field.value, field.back = addPath(field.value, suggestions[choice-1]), 0
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
		f.syncSchedules()
	case fieldTime, fieldDay:
		owner := f.field(strings.TrimSuffix(strings.TrimSuffix(field.key, "_time"), "_day"))
		timeSteps, dayDelta := delta, 0
		if field.kind == fieldDay {
			timeSteps, dayDelta = 0, delta
		}
		if adjusted, ok := schedule.Adjust(owner.value, timeSteps, dayDelta); ok {
			owner.value = adjusted
		}
		f.syncSchedules()
	case fieldChecklist:
		field.sub = min(max(field.sub+delta, 0), max(len(field.choices)-1, 0))
	case fieldRetention:
		field.sub = min(max(field.sub+delta, 0), len(retentionKeys)-1)
	}
	f.touched[field.key] = true
}

func (f *form) toggle(index, sub int) {
	field := &f.fields[index]
	if field.kind == fieldToggle {
		f.advancedOpen = !f.advancedOpen
		return
	}
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
	m.form.syncSchedules()
	return m, true
}

func (m manageModel) saveForm() (tea.Model, tea.Cmd) {
	m.form.attempted = true
	label, err := m.form.apply(m.actions)
	var invalid *configedit.InvalidError
	switch {
	case err == nil:
		verb := "Đã thêm"
		if m.form.original != "" {
			verb = "Đã lưu"
		}
		m = m.pop()
		m.notice, m.noticeBad = fmt.Sprintf("%s %s vào config. Khởi động lại daemon (sentinel run) để áp dụng.", verb, label), false
		m = m.refreshJobRepositories().refreshConfig()
		return m, m.reload()
	case errors.As(err, &invalid), errors.Is(err, configedit.ErrExists), errors.Is(err, configedit.ErrNotFound):
		if errors.As(err, &invalid) && invalid.Field != "" {
			for i, field := range m.form.fields {
				if field.key == invalid.Field {
					if field.section == sectionAdvanced {
						m.form.advancedOpen = true // the field with the problem must be in view
					}
					m.form.focusOn(i)
					break
				}
			}
		} else {
			m.form.problem = err.Error()
		}
	default:
		// Not the user's input: leave the form so the failure is not mistaken for a validation message.
		m = m.pop()
		m.notice, m.noticeBad = "Không lưu được config: "+err.Error(), true
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
		label += ", backup lưu tại " + repo.URL
		if strings.Contains(repo.URL, ":") { // a remote location: the repository is not created behind anyone's back
			label += " (tạo repository một lần bằng: sentinel backup init " + job.Name + ")"
		} else {
			label += " (repository sẽ được tạo ở lần backup đầu tiên)"
		}
	}
	if err := actions.saveJob(f.original, job); err != nil {
		if repo.URL != "" {
			err = fmt.Errorf("đã tạo repository %s nhưng chưa lưu được job: %w", repo.Name, err)
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

// onPaste puts pasted text into the text field of the open form; anywhere else a paste does nothing.
func (m manageModel) onPaste(content string) manageModel {
	if !m.formOpen() {
		return m
	}
	field := &m.form.fields[m.form.focus]
	if !field.editsText() {
		return m
	}
	field.edit(tea.PasteMsg{Content: content})
	m.form.touched[field.key] = true
	m.form.problem = ""
	return m
}
