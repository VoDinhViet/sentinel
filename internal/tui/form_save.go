package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/configedit"
)

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
		fieldFocused := errors.As(err, &invalid) && m.form.focusOnField(invalid.Field)
		if !fieldFocused {
			m.form.problem = err.Error() // no field to show it under: it is the problem of the whole save
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
		if err := actions.saveRepository("", repo); err != nil && !createdBefore(actions, repo, err) {
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

// createdBefore says err only tells that the repository is there already, with the same name and place:
// a try of this form that created it and then failed to save the job, so the next try goes on with it.
func createdBefore(actions manageActions, repo configedit.Repository, err error) bool {
	if !errors.Is(err, configedit.ErrExists) || actions.overview == nil {
		return false
	}
	overview, overviewErr := actions.overview()
	if overviewErr != nil {
		return false
	}
	return slices.ContainsFunc(overview.Repositories, func(existing configedit.Repository) bool {
		return existing.Name == repo.Name && existing.URL == repo.URL
	})
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
	before := m.form.connectionKey()
	field.edit(tea.PasteMsg{Content: content})
	m.form.touched[field.key] = true
	m.form.problem = ""
	if m.form.connectionKey() != before {
		m.form.connect = connectState{} // what the test found is about other values
	}
	return m
}
