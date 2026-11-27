package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/configedit"
	"sentinel/agent/internal/schedule"
	"sentinel/agent/internal/uptime"
	"sentinel/pkg/notify"
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

// uptimeEndpoint is the address an uptime entry probes, written the way `sentinel check` and the
// alerts write it: the secrets of an URL are masked. The entry is the raw text of the file, so a URL
// that still holds a ${VAR} reference (no secret value in it) is shown as written, and an entry
// without a known type shows its first address field, as it did before.
func uptimeEndpoint(check configedit.Uptime) string {
	switch {
	case check.Type == config.CheckHTTP && strings.Contains(check.URL, "${"):
		return check.URL
	case check.Type == config.CheckHTTP, check.Type == config.CheckTCP, check.Type == config.CheckPing:
		return uptime.Endpoint(config.UptimeCheck{Type: check.Type, URL: check.URL, Address: check.Address, Host: check.Host})
	default:
		return orDefault(check.URL, orDefault(check.Address, check.Host))
	}
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

// configSections are the groups of the Config tab, in the order of configSection: what each
// group is for, and the columns that suit it.
var configSections = [...]struct {
	title, hint string
	headers     []string
	empty       string // the row that invites adding the first entry of an empty group
}{
	secRepository: {"REPOSITORY", "nơi lưu các bản backup", []string{"TÊN", "ĐỊA CHỈ", "MẬT KHẨU", "DÙNG BỞI"}, "chưa có repository nào"},
	secJob:        {"JOB", "những gì được backup, và khi nào", []string{"TÊN", "LOẠI", "LỊCH", "REPOSITORY", "GIỮ"}, "chưa có job nào"},
	secUptime:     {"UPTIME CHECK", "kiểm tra dịch vụ còn sống", []string{"TÊN", "LOẠI", "ĐÍCH", "MỖI"}, "chưa có uptime check nào"},
	secNotifier:   {"NOTIFIER", "nơi gửi cảnh báo", []string{"KÊNH", "CẤU HÌNH"}, "chưa có nơi nhận cảnh báo nào"},
	secSettings:   {"CÀI ĐẶT CHUNG", "tên máy và giá trị mặc định của job", []string{"TÊN MÁY", "LỊCH", "REPOSITORY", "RETENTION"}, ""},
}

func (m manageModel) buildConfigTab() manageTab {
	tab := manageTab{title: "Config", headers: configSections[secRepository].headers, statusCol: -1, historyCol: -1}
	overview, listing := m.config.overview, m.config.listing
	// begin opens the group of a section; finish adds the invitation to an empty one.
	begin := func(section configSection) {
		heading := configSections[section]
		tab.sections = append(tab.sections, tableSection{title: heading.title, hint: heading.hint, headers: heading.headers, first: len(tab.lines)})
	}
	finish := func(section configSection) {
		if first := tab.sections[len(tab.sections)-1].first; first == len(tab.lines) {
			text := configSections[section].empty + ": bấm a để thêm"
			tab.lines = append(tab.lines, manageLine{cells: []string{text}, detail: text, entry: configEntry{section: section}, placeholder: true})
		}
	}
	add := func(entry configEntry, level severity, cells []string, detail string, fields [][2]string) {
		tab.lines = append(tab.lines, manageLine{cells: cells, severity: level, detail: detail, entry: entry, fields: fields})
	}

	begin(secRepository)
	for _, repository := range overview.Repositories {
		level, password := sevOK, envOrNone(repository.PasswordEnv, repository.PasswordLiteral)
		if repository.PasswordLiteral {
			level = sevWarn
		}
		users := "chưa job nào dùng"
		if len(repository.UsedBy) > 0 {
			users = "dùng bởi " + strings.Join(repository.UsedBy, ", ")
		}
		detail := fmt.Sprintf("Repository %s: %s · mật khẩu %s · %s", repository.Name, repository.URL, password, users)
		if repository.PasswordLiteral {
			detail += ". Mật khẩu đang ghi thẳng trong file: bấm sửa và nhập tên env var để thay."
		}
		add(configEntry{secRepository, repository.Name}, level, []string{repository.Name, repository.URL, password, users}, detail,
			[][2]string{{"Địa chỉ", repository.URL}, {"Mật khẩu", password}, {"Dùng bởi", strings.TrimPrefix(users, "dùng bởi ")}})
	}
	finish(secRepository)

	begin(secJob)
	for _, job := range listing.Jobs {
		repositories, keep := strings.Join(job.Repositories, ", "), retentionText(job.Retention)
		if job.Inherited.Repositories {
			repositories = strings.Join(listing.InheritedRepositories, ", ") + " (mặc định)"
		}
		if job.Inherited.Retention {
			keep = retentionText(overview.Settings.DefaultRetention) + " (mặc định)"
		}
		when := m.whenText(job.Schedule)
		add(configEntry{secJob, job.Name}, sevOK, []string{job.Name, job.Type, when, repositories, keep},
			fmt.Sprintf("Job %s: %s, %s, lưu vào %s, giữ %s.", job.Name, job.Type, when, repositories, keep),
			m.jobSetupFields(job, repositories, keep))
	}
	finish(secJob)

	begin(secUptime)
	for _, check := range overview.Uptime {
		endpoint := uptimeEndpoint(check)
		every := orDefault(check.Interval, "1m") + " (mặc định)"
		if check.Interval != "" {
			every = check.Interval
		}
		fields := [][2]string{{"Loại", check.Type}, {"Đích", endpoint}, {"Mỗi", every}}
		if check.Timeout != "" {
			fields = append(fields, [2]string{"Timeout", check.Timeout})
		}
		add(configEntry{secUptime, check.Name}, sevOK, []string{check.Name, check.Type, endpoint, "mỗi " + every},
			fmt.Sprintf("Uptime check %s: %s %s, mỗi %s.", check.Name, check.Type, endpoint, every), fields)
	}
	finish(secUptime)

	begin(secNotifier)
	for _, notifier := range overview.Notifiers {
		var parts []string
		var fields [][2]string
		for _, spec := range notify.FieldsOf(notifier.Kind) {
			switch {
			case spec.Secret:
				value := envOrNone(notifier.Values[spec.Key], notifier.Literal[spec.Key])
				parts, fields = append(parts, spec.Key+" "+value), append(fields, [2]string{spec.Label, value})
			case notifier.Values[spec.Key] != "":
				parts, fields = append(parts, spec.Key+" "+notifier.Values[spec.Key]), append(fields, [2]string{spec.Label, notifier.Values[spec.Key]})
			}
		}
		level := sevOK
		for _, literal := range notifier.Literal {
			if literal {
				level = sevWarn // a secret written in the file
			}
		}
		add(configEntry{secNotifier, notifier.Kind}, level, []string{notifier.Kind, strings.Join(parts, " · ")}, "Notifier "+notifier.Kind+": "+strings.Join(parts, " · "), fields)
	}
	finish(secNotifier)

	begin(secSettings)
	settings := overview.Settings
	defaults := "chưa có mặc định"
	if settings.DefaultSchedule != "" || len(settings.DefaultRepositories) > 0 || !settings.DefaultRetention.Empty() {
		defaults = "mặc định: " + strings.Join(nonEmpty(settings.DefaultSchedule, strings.Join(settings.DefaultRepositories, ", "), settings.DefaultRetention.String()), " · ")
	}
	serverName := orDefault(settings.ServerName, "(tên máy)")
	verifySchedule := schedule.Describe(orDefault(settings.VerifySchedule, config.DefaultVerifySchedule))
	pruneSchedule := schedule.Describe(orDefault(settings.PruneSchedule, config.DefaultPruneSchedule))
	defaultSchedule := orDefault(settings.DefaultSchedule, "chưa đặt")
	defaultRepositories := orDefault(strings.Join(settings.DefaultRepositories, ", "), "chưa đặt")
	defaultRetention := orDefault(strings.Join(nonEmpty(settings.DefaultRetention.String()), ""), "chưa đặt")
	add(configEntry{secSettings, "general"}, sevOK, []string{serverName, defaultSchedule, defaultRepositories, defaultRetention},
		fmt.Sprintf("Cài đặt: server %s, cảnh báo lặp lại mỗi %s, kiểm tra repository %s. %s", serverName, orDefault(settings.Cooldown, "1h (mặc định)"), verifySchedule, defaults),
		[][2]string{
			{"Tên máy", serverName}, {"Cảnh báo lặp lại", orDefault(settings.Cooldown, "1h (mặc định)")}, {"Kiểm tra repo", verifySchedule}, {"Dọn repo", pruneSchedule},
			{"Lịch mặc định", defaultSchedule}, {"Repository mặc định", defaultRepositories}, {"Retention mặc định", defaultRetention},
		})
	tab.count = 0
	if m.config.err != nil {
		return manageTab{title: "Config", headers: tab.headers, statusCol: -1, historyCol: -1}
	}
	return tab
}

// jobSetupFields are the label/value pairs that say how a job is set up.
func (m manageModel) jobSetupFields(job configedit.Job, repositories, keep string) [][2]string {
	source := strings.Join(job.Paths, ", ")
	if job.Type != "files" {
		source = strings.Trim(job.Host+" / "+job.Database, " /")
	}
	nextRun, _ := m.nextRun(job.Name)
	return [][2]string{
		{"Loại", job.Type}, {"Nguồn", source}, {"Repository", repositories},
		{"Lịch", m.whenText(job.Schedule)}, {"Lần tới", nextRun}, {"Giữ", keep},
	}
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
		for _, kind := range notify.Kinds() {
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
	if entry.name == "" { // the invitation row of an empty group: there is nothing to edit yet
		return m.configAdd()
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
	if entry.name == "" {
		m.notice, m.noticeBad = "Nhóm này chưa có mục nào để xoá: bấm a để thêm.", false
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

// configTestNotifier sends a test message through the selected notifier, in the background.
func (m manageModel) configTestNotifier() (tea.Model, tea.Cmd) {
	entry, ok := m.selectedConfig()
	if !ok || entry.section != secNotifier || entry.name == "" {
		m.notice, m.noticeBad = "Chọn một dòng Notifier rồi bấm t để gửi thử.", false
		return m, nil
	}
	if m.actions.testNotifier == nil {
		m.notice, m.noticeBad = "Không gửi thử được ở màn hình này.", true
		return m, nil
	}
	ctx := m.startBusy("Đang gửi tin thử tới " + entry.name)
	kind, actions := entry.name, m.actions
	return m, tea.Batch(m.spin(), func() tea.Msg {
		return notifierTestedMsg{kind: kind, err: actions.testNotifier(ctx, kind)}
	})
}

func (m manageModel) afterNotifierTest(msg notifierTestedMsg) manageModel {
	m.endBusy()
	if msg.err != nil {
		m.notice, m.noticeBad = fmt.Sprintf("Gửi thử tới %s thất bại: %v", msg.kind, msg.err), true
		return m
	}
	m.notice, m.noticeBad = "Đã gửi tin thử tới "+msg.kind+": kiểm tra xem tin đã tới chưa.", false
	return m
}
