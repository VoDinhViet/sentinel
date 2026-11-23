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
// channels and settings of the config file, each editable in place.
const configTab = 4

type configSection int

const (
	secRepository configSection = iota
	secJob
	secUptime
	secNotifier
	secSettings
)

var sectionNames = [...]string{"Repository", "Job", "Uptime", "Notifier", "Settings"}

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
		m.config.err = errors.New("the config file is not available here")
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
		return "written in the file"
	}
	return "none"
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
			return schedule.Describe(defaultSpec) + " (default)"
		}
	}
	return schedule.Describe(spec)
}

func (m manageModel) buildConfigTab() manageTab {
	tab := manageTab{title: "Config", headers: []string{"SECTION", "NAME", "WHAT", "DETAILS"}, statusCol: -1, historyCol: -1}
	add := func(entry configEntry, level severity, what, details, detail string) {
		tab.lines = append(tab.lines, manageLine{
			cells:    []string{sectionNames[entry.section], entry.name, what, details},
			severity: level, detail: detail, entry: entry,
		})
	}
	overview, listing := m.config.overview, m.config.listing
	for _, repository := range overview.Repositories {
		level, password := sevOK, "password "+envOrNone(repository.PasswordEnv, repository.PasswordLiteral)
		if repository.PasswordLiteral {
			level = sevWarn
		}
		users := "not used by any job"
		if len(repository.UsedBy) > 0 {
			users = "used by " + strings.Join(repository.UsedBy, ", ")
		}
		detail := fmt.Sprintf("Repository %s: %s · %s · %s", repository.Name, repository.URL, password, users)
		if repository.PasswordLiteral {
			detail += ". The password is written in the file: edit and name an environment variable to replace it."
		}
		add(configEntry{secRepository, repository.Name}, level, repository.URL, password+" · "+users, detail)
	}
	for _, job := range listing.Jobs {
		repositories, keep := strings.Join(job.Repositories, ", "), job.Retention.String()
		if job.Inherited.Repositories {
			repositories = strings.Join(listing.InheritedRepositories, ", ") + " (default)"
		}
		if job.Inherited.Retention {
			keep = overview.Settings.DefaultRetention.String() + " (default)"
		}
		when := m.whenText(job.Schedule)
		add(configEntry{secJob, job.Name}, sevOK, job.Type+" · "+when, "→ "+repositories+" · keep "+keep,
			fmt.Sprintf("Job %s: %s, %s, stored in %s, keeping %s.", job.Name, job.Type, when, repositories, keep))
	}
	for _, check := range overview.Uptime {
		target := orDefault(check.URL, orDefault(check.Address, check.Host))
		every := orDefault(check.Interval, "1m") + " (default)"
		if check.Interval != "" {
			every = check.Interval
		}
		add(configEntry{secUptime, check.Name}, sevOK, check.Type+" "+target, "every "+every, fmt.Sprintf("Uptime check %s: %s %s, every %s.", check.Name, check.Type, target, every))
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
		add(configEntry{secNotifier, notifier.Kind}, level, "alerts go here", strings.Join(parts, " · "), "Notification channel "+notifier.Kind+": "+strings.Join(parts, " · "))
	}
	settings := overview.Settings
	defaults := "no defaults"
	if settings.DefaultSchedule != "" || len(settings.DefaultRepositories) > 0 || !settings.DefaultRetention.Empty() {
		defaults = "defaults: " + strings.Join(nonEmpty(settings.DefaultSchedule, strings.Join(settings.DefaultRepositories, ", "), settings.DefaultRetention.String()), " · ")
	}
	add(configEntry{secSettings, "general"}, sevOK, "server "+orDefault(settings.ServerName, "(host name)"), defaults,
		fmt.Sprintf("Settings: server %s, alerts repeat every %s, repository check %s. %s", orDefault(settings.ServerName, "(host name)"), orDefault(settings.Cooldown, "1h (default)"), schedule.Describe(settings.RepoCheckSchedule), defaults))
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
			m.notice, m.noticeBad = "Every kind of channel is configured already: edit one instead.", true
			return m
		}
		return m.openEntryForm(newNotifierForm(next, ""))
	}
	m.notice, m.noticeBad = "Settings is a single entry: press e to edit it.", false
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
	m.notice, m.noticeBad = entry.name+" is not in the config file any more.", true
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
				m.notice, m.noticeBad = fmt.Sprintf("Repository %s is used by %s: change them first.", entry.name, strings.Join(repository.UsedBy, ", ")), true
				return m
			}
		}
		return m.confirmRemoval("repository", entry.name, "The backups already stored there stay where they are.", func() error { return actions.deleteRepository(entry.name) })
	case secUptime:
		return m.confirmRemoval("uptime check", entry.name, "Its recorded history ages out of the store by itself.", func() error { return actions.deleteUptime(entry.name) })
	case secNotifier:
		return m.confirmRemoval("notification channel", entry.name, "Alerts stop going there.", func() error { return actions.deleteNotifier(entry.name) })
	}
	m.notice, m.noticeBad = "The settings cannot be removed: press e to change them.", false
	return m
}
