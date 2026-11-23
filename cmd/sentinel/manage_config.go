package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"sentinel/internal/configedit"
	"sentinel/internal/schedule"
)

// configTab is the index of the Config tab: the repositories, jobs, uptime checks, notification
// notifiers and settings of the config file, each editable in place.
const configTab = 4

type configSection int

const (
	secRepository configSection = iota
	secJob
	secUptime
	secNotifier
	secSettings
)

var sectionNames = [...]string{"Repository", "Job", "Uptime", "Notifier", "Cài đặt"}

// configEntry says which entry of the config a line of the Config tab is.
type configEntry struct {
	section configSection
	name    string
}

// configData is what the Config tab shows, read from the config file.
type configData struct {
	loaded   bool
	err      error
	overview configedit.Overview
	listing  configedit.Listing
}

// refreshConfig reads the config file again (after a save, a removal or the first visit).
func (m manageModel) refreshConfig() manageModel {
	m.config = configData{loaded: true}
	if m.actions.overview == nil || m.actions.listJobs == nil {
		m.config.err = errors.New("file config không dùng được ở đây")
		return m
	}
	if m.config.overview, m.config.err = m.actions.overview(); m.config.err != nil {
		return m
	}
	m.config.listing, m.config.err = m.actions.listJobs()
	return m
}

// envOrNone is "${NAME}" for a variable name, or a short note for a literal or missing one.
func envOrNone(name string, literal bool) string {
	switch {
	case name != "":
		return "${" + name + "}"
	case literal:
		return "ghi thẳng trong file"
	}
	return "chưa có"
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// whenText says in words when a schedule runs; an empty one means the default, or by hand.
func (m manageModel) whenText(spec string) string {
	if spec == "" {
		if defaultSpec := m.config.overview.Settings.DefaultSchedule; defaultSpec != "" {
			return schedule.Describe(defaultSpec) + " (mặc định)"
		}
	}
	return schedule.Describe(spec)
}

func (m manageModel) buildConfigTab() manageTab {
	tab := manageTab{title: "Config", headers: []string{"MỤC", "TÊN", "LÀ GÌ", "CHI TIẾT"}, statusCol: -1, historyCol: -1}
	add := func(entry configEntry, level severity, what, details, detail string) {
		tab.lines = append(tab.lines, manageLine{
			cells:    []string{sectionNames[entry.section], entry.name, what, details},
			severity: level, detail: detail, entry: entry,
		})
	}
	overview, listing := m.config.overview, m.config.listing
	for _, repository := range overview.Repositories {
		level, password := sevOK, "mật khẩu "+envOrNone(repository.PasswordEnv, repository.PasswordLiteral)
		if repository.PasswordLiteral {
			level = sevWarn
		}
		users := "chưa job nào dùng"
		if len(repository.UsedBy) > 0 {
			users = "dùng bởi " + strings.Join(repository.UsedBy, ", ")
		}
		detail := fmt.Sprintf("Repository %s: %s · %s · %s", repository.Name, repository.URL, password, users)
		if repository.PasswordLiteral {
			detail += ". Mật khẩu đang ghi thẳng trong file: bấm sửa và nhập tên env var để thay."
		}
		add(configEntry{secRepository, repository.Name}, level, repository.URL, password+" · "+users, detail)
	}
	for _, job := range listing.Jobs {
		repositories, keep := strings.Join(job.Repositories, ", "), job.Retention.String()
		if job.Inherited.Repositories {
			repositories = strings.Join(listing.InheritedRepositories, ", ") + " (mặc định)"
		}
		if job.Inherited.Retention {
			keep = overview.Settings.DefaultRetention.String() + " (mặc định)"
		}
		when := m.whenText(job.Schedule)
		add(configEntry{secJob, job.Name}, sevOK, job.Type+" · "+when, "→ "+repositories+" · giữ "+keep,
			fmt.Sprintf("Job %s: %s, %s, lưu vào %s, giữ %s.", job.Name, job.Type, when, repositories, keep))
	}
	for _, check := range overview.Uptime {
		target := orDefault(check.URL, orDefault(check.Address, check.Host))
		every := orDefault(check.Interval, "1m") + " (mặc định)"
		if check.Interval != "" {
			every = check.Interval
		}
		add(configEntry{secUptime, check.Name}, sevOK, check.Type+" "+target, "mỗi "+every, fmt.Sprintf("Uptime check %s: %s %s, mỗi %s.", check.Name, check.Type, target, every))
	}
	for _, notifier := range overview.Notifiers {
		var parts []string
		for _, spec := range configedit.NotifierFields(notifier.Kind) {
			switch {
			case spec.Secret:
				parts = append(parts, spec.Key+" "+envOrNone(notifier.Values[spec.Key], notifier.Literal[spec.Key]))
			case notifier.Values[spec.Key] != "":
				parts = append(parts, spec.Key+" "+notifier.Values[spec.Key])
			}
		}
		level := sevOK
		for _, literal := range notifier.Literal {
			if literal {
				level = sevWarn // a secret written in the file
			}
		}
		add(configEntry{secNotifier, notifier.Kind}, level, "cảnh báo gửi vào đây", strings.Join(parts, " · "), "Notifier "+notifier.Kind+": "+strings.Join(parts, " · "))
	}
	settings := overview.Settings
	defaults := "chưa có mặc định"
	if settings.DefaultSchedule != "" || len(settings.DefaultRepositories) > 0 || !settings.DefaultRetention.Empty() {
		defaults = "mặc định: " + strings.Join(nonEmpty(settings.DefaultSchedule, strings.Join(settings.DefaultRepositories, ", "), settings.DefaultRetention.String()), " · ")
	}
	add(configEntry{secSettings, "general"}, sevOK, "server "+orDefault(settings.ServerName, "(tên máy)"), defaults,
		fmt.Sprintf("Cài đặt: server %s, cảnh báo lặp lại mỗi %s, kiểm tra repository %s. %s", orDefault(settings.ServerName, "(tên máy)"), orDefault(settings.Cooldown, "1h (mặc định)"), schedule.Describe(settings.VerifySchedule), defaults))
	if m.config.err != nil {
		return manageTab{title: "Config", headers: tab.headers, statusCol: -1, historyCol: -1}
	}
	return tab
}

func nonEmpty(values ...string) []string {
	return slices.DeleteFunc(values, func(value string) bool { return value == "" || value == "keep all" })
}

// selectedConfig is the entry under the cursor on the Config tab.
func (m manageModel) selectedConfig() (configEntry, bool) {
	lines := m.tabs()[m.tab].lines
	if m.tab != configTab || m.cursor >= len(lines) {
		return configEntry{}, false
	}
	return lines[m.cursor].entry, true
}

// configAdd opens the form for a new entry in the section of the selected line.
func (m manageModel) configAdd() manageModel {
	entry, ok := m.selectedConfig()
	if !ok {
		return m
	}
	listing := m.config.listing
	switch entry.section {
	case secJob:
		return m.startAdd()
	case secRepository:
		fresh := configedit.Repository{PasswordEnv: "RESTIC_PASSWORD", Env: map[string]string{awsKeyID: awsKeyID, awsSecretKey: awsSecretKey}}
		return m.openEntryForm(newRepositoryForm(fresh, "", listing))
	case secUptime:
		return m.openEntryForm(newUptimeForm(configedit.Uptime{}, ""))
	case secNotifier:
		next := configedit.Notifier{}
		for _, kind := range configedit.NotifierKinds {
			if !slices.ContainsFunc(m.config.overview.Notifiers, func(existing configedit.Notifier) bool { return existing.Kind == kind }) {
				next.Kind = kind
				break
			}
		}
		if next.Kind == "" {
			m.notice, m.noticeBad = "Đã cấu hình đủ mọi notifier: hãy sửa một cái có sẵn.", true
			return m
		}
		return m.openEntryForm(newNotifierForm(next, ""))
	}
	m.notice, m.noticeBad = "Cài đặt chỉ có một mục: bấm e để sửa.", false
	return m
}

// configEdit opens the form for the selected entry.
func (m manageModel) configEdit() manageModel {
	entry, ok := m.selectedConfig()
	if !ok {
		return m
	}
	overview, listing := m.config.overview, m.config.listing
	switch entry.section {
	case secJob:
		return m.editJob(entry.name)
	case secRepository:
		for _, repository := range overview.Repositories {
			if repository.Name == entry.name {
				return m.openEntryForm(newRepositoryForm(repository, entry.name, listing))
			}
		}
	case secUptime:
		for _, check := range overview.Uptime {
			if check.Name == entry.name {
				return m.openEntryForm(newUptimeForm(check, entry.name))
			}
		}
	case secNotifier:
		for _, notifier := range overview.Notifiers {
			if notifier.Kind == entry.name {
				return m.openEntryForm(newNotifierForm(notifier, entry.name))
			}
		}
	case secSettings:
		return m.openEntryForm(newSettingsForm(overview.Settings, listing))
	}
	m.notice, m.noticeBad = entry.name+" không còn trong file config nữa.", true
	return m
}

// configRemove asks for the name of the selected entry before removing it.
func (m manageModel) configRemove() manageModel {
	entry, ok := m.selectedConfig()
	if !ok {
		return m
	}
	actions := m.actions
	switch entry.section {
	case secJob:
		return m.deleteJobNamed(entry.name)
	case secRepository:
		for _, repository := range m.config.overview.Repositories {
			if repository.Name == entry.name && len(repository.UsedBy) > 0 {
				m.notice, m.noticeBad = fmt.Sprintf("Repository %s đang được dùng bởi %s: hãy đổi các job đó trước.", entry.name, strings.Join(repository.UsedBy, ", ")), true
				return m
			}
		}
		return m.confirmRemoval("repository", entry.name, "Các backup đã lưu ở đó vẫn giữ nguyên.", func() error { return actions.deleteRepository(entry.name) })
	case secUptime:
		return m.confirmRemoval("uptime check", entry.name, "Lịch sử đã ghi sẽ tự hết hạn khỏi file trạng thái.", func() error { return actions.deleteUptime(entry.name) })
	case secNotifier:
		return m.confirmRemoval("notifier", entry.name, "Cảnh báo sẽ không gửi vào đó nữa.", func() error { return actions.deleteNotifier(entry.name) })
	}
	m.notice, m.noticeBad = "Không xoá được cài đặt: bấm e để đổi.", false
	return m
}
