package main

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
)

// refreshJobRepositories re-reads the config so new jobs show up (and removed ones go).
func (m manageModel) refreshJobRepositories() manageModel {
	if m.actions.listJobs == nil {
		return m
	}
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
		m.notice, m.noticeBad = "Không đọc được config: "+err.Error(), true
		return m
	}
	return m.openForm(configedit.Job{Type: config.JobPostgres}, "", listing)
}

// startEdit opens the form for the selected job on the Backups tab.
func (m manageModel) startEdit() manageModel {
	line, ok := m.selectedBackup()
	if !ok {
		return m
	}
	return m.editJob(line.job)
}

// editJob opens the form for the named job, or says why it cannot.
func (m manageModel) editJob(name string) manageModel {
	listing, err := m.actions.listJobs()
	if err != nil {
		m.notice, m.noticeBad = "Không đọc được config: "+err.Error(), true
		return m
	}
	for _, job := range listing.Jobs {
		if job.Name != name {
			continue
		}
		if !job.Editable() {
			m.notice, m.noticeBad = fmt.Sprintf("%s là job loại %s, màn hình này không sửa được. Hãy sửa file config.", job.Name, job.Type), true
			return m
		}
		return m.openForm(job, job.Name, listing)
	}
	m.notice, m.noticeBad = name+" không còn trong file config nữa.", true
	return m
}

// startDelete asks for the job name before removing the selected job of the Backups tab.
func (m manageModel) startDelete() manageModel {
	line, ok := m.selectedBackup()
	if !ok {
		return m
	}
	return m.deleteJobNamed(line.job)
}

// deleteJobNamed asks for the job name before removing it from the config.
func (m manageModel) deleteJobNamed(name string) manageModel {
	listing, err := m.actions.listJobs()
	if err != nil {
		m.notice, m.noticeBad = "Không đọc được config: "+err.Error(), true
		return m
	}
	for _, job := range listing.Jobs {
		if job.Name == name && job.Editable() {
			actions := m.actions
			return m.confirmRemoval("job", name, "Các snapshot đã có vẫn nằm trong repository.", func() error { return actions.deleteJob(name) })
		}
	}
	m.notice, m.noticeBad = name+" không phải là job mà màn hình này xoá được (không có trong file config, hoặc thuộc loại khác).", true
	return m
}

// confirmRemoval opens the screen that asks for the entry's name before remove runs.
func (m manageModel) confirmRemoval(what, name, note string, remove func() error) manageModel {
	m.mode, m.deleteWhat, m.deleteName, m.deleteNote, m.deleteAction, m.confirm, m.inputError = modeDelete, what, name, note, remove, "", ""
	return m
}

func (m manageModel) onDeleteKey(key, text string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc":
		m.mode = modeTable
	case "enter":
		if m.confirm != m.deleteName {
			m.inputError = "tên không khớp"
			return m, nil
		}
		if err := m.deleteAction(); err != nil {
			m.mode, m.notice, m.noticeBad = modeTable, "Không xoá được "+m.deleteWhat+": "+err.Error(), true
			return m, nil
		}
		m.mode, m.notice, m.noticeBad = modeTable, fmt.Sprintf("Đã xoá %s %s khỏi config. %s Khởi động lại daemon (sentinel run) để áp dụng.", m.deleteWhat, m.deleteName, m.deleteNote), false
		m = m.refreshJobRepositories().refreshConfig()
		m.cursor = max(min(m.cursor, len(m.tabs()[m.tab].lines)-1), 0)
		return m, m.reload()
	default:
		m.confirm, m.inputError = editText(m.confirm, key, text), ""
	}
	return m, nil
}

func (m manageModel) renderDelete() []string {
	screenLines := []string{
		titleStyle.Render("Xoá " + m.deleteName),
		"",
		wrap(fmt.Sprintf("Thao tác này xoá %s khỏi file config. %s", m.deleteWhat, m.deleteNote), m.width-2),
		"",
		"Gõ tên (" + m.deleteName + ") để xác nhận:",
		"  " + m.confirm + lipglossAccent("▏"),
	}
	if m.inputError != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(m.inputError))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "enter", "xoá", "esc", "huỷ"))
}
