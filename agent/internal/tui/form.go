package tui

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/configedit"
	"sentinel/agent/internal/schedule"
)

// The sections of a job form, in the order they are drawn.
const (
	sectionBasic      = "CƠ BẢN"
	sectionConnection = "KẾT NỐI"
	sectionSchedule   = "LỊCH & GIỮ BẢN"
	sectionAdvanced   = "NÂNG CAO"
)

type fieldKind int

const (
	fieldText      fieldKind = iota
	fieldNumber              // digits only
	fieldChoice              // one of choices, with ‹ ›
	fieldChecklist           // several of choices, each ticked or not
	fieldSchedule            // text with presets on ← →, and a plain-words preview
	fieldRetention           // five counts on one line
	fieldPaths               // comma separated folders, with suggestions
	fieldToggle              // a line that opens or closes the advanced fields below it
	fieldTime                // the time of day of the schedule field above it (key <schedule>_time)
	fieldDay                 // the weekday or day of month of that schedule (key <schedule>_day)
)

// What the Storage choice of a job form means.
const (
	storageAuto       = "Tự động"
	storageFolder     = "Thư mục"
	storageRepository = "Repository"
)

var retentionKeys = [5]string{"last", "daily", "weekly", "monthly", "yearly"}

// retentionUnits are what each retention count counts, in the words of the screen, in the order of retentionKeys.
var retentionUnits = [5]string{"bản mới nhất", "ngày", "tuần", "tháng", "năm"}

// scheduleHint says what a schedule field accepts and which keys step through it.
const scheduleHint = `gõ trực tiếp ("daily 2am", "weekly monday 3am", "every 6 hours", cron) · ← → đổi tần suất · alt+↑↓ giờ · alt+←→ thứ hoặc ngày`

// formField is one input of a form.
type formField struct {
	key     string
	label   string
	hint    string
	kind    fieldKind
	value   string // text, number, choice and schedule
	back    int    // text, number and paths: how many characters follow the cursor (see textedit.go)
	choices []string
	ticked  []bool    // checklist: one per choice
	counts  [5]string // retention: last daily weekly monthly yearly
	sub     int       // checklist item or retention count under the cursor
	// placeholder is what an empty field stands for, shown dimmed.
	placeholder string
	// placeholderOwn says the placeholder is the field's own setting that this input cannot show
	// (a rule of daily and weekly counts), not the default it falls back to: it is drawn as a value.
	placeholderOwn bool
	// unit is what the number counts ("bản mới nhất"), shown after the input; the placeholder then
	// goes after the unit too instead of inside the box.
	unit  string
	fixed bool // a choice that cannot be changed
	// complete makes Tab finish a path typed in the field (a Paths field always does);
	// options are the candidates the last Tab listed.
	complete bool
	options  []string
	// section groups the fields of a job form under a heading ("" for no group).
	section string
	// onField and only show the field only while the field named onField holds one of only.
	onField string
	only    []string
}

// formDefaults are the config's defaults, shown on the fields they fill in.
type formDefaults struct {
	schedule     string
	repositories []string
	retention    config.Retention
}

// formKind says what a form edits.
type formKind int

const (
	formJob formKind = iota
	formRepository
	formUptime
	formNotifier
	formSettings
	formInit // the first-run form of `sentinel init`
)

// form edits one entry of the config: a job (a database dump or a set of files), a
// repository, an uptime check, a notifier notifier or the general settings. original is the job's name when editing, "" when adding.
type form struct {
	kind         formKind
	title        string
	original     string
	passwordKept bool // the file holds a literal password that stays unless an env var is entered
	// originalRetention is the retention the job had when the form opened: a database job keeps it
	// unless a number is typed into its "keep" input.
	originalRetention config.Retention
	known             configedit.Known
	defaults          formDefaults
	fields            []formField
	focus             int
	advancedOpen      bool            // the advanced section of a job form is unfolded
	touched           map[string]bool // fields the user has been in: only those show their problem
	attempted         bool            // a save was tried: every problem shows
	problem           string          // a problem of the whole save, not of one field

	// kept is what a repository form does not edit and must write back as it was.
	kept map[string]string

	connect connectState // the last connection test of a database job

	disk *diskMemo // what the form read from this machine; nil = read again at every draw
}

// connectState is where the connection test of a database job form stands.
type connectState struct {
	seq     int // the test this state belongs to (0 = none)
	running bool
	text    string // what the test found, shown beside the button
	bad     bool
}

// connectionKeys are the fields whose value decides whether the database can be reached: a
// result for other values is stale and is dropped.
var connectionKeys = []string{"type", "database", "host", "port", "user", "password_env", "tls", "auth_database",
	"job_type", "job_database", "job_password_env"}

// connectionKey is the connection fields' values joined, to tell when a test no longer applies.
func (f form) connectionKey() string {
	var parts []string
	for _, key := range connectionKeys {
		parts = append(parts, f.field(key).value)
	}
	return strings.Join(parts, "\x00")
}

// databaseJob is the type of the job being edited when it is a database job, "" otherwise.
func (f form) databaseType() string {
	key := "type"
	if f.kind == formInit {
		key = "job_type"
	}
	if f.kind != formJob && f.kind != formInit {
		return ""
	}
	if jobType := f.field(key).value; slices.Contains(configedit.DatabaseTypes, jobType) {
		return jobType
	}
	return ""
}

// connectionJob is the job whose database the test connects to, as the form holds it.
func (f form) connectionJob() (configedit.Job, error) {
	if f.kind == formInit {
		initial, err := f.initial()
		return initial.Job, err
	}
	return f.job()
}

func newJobForm(job configedit.Job, original string, listing configedit.Listing) form {
	keepValue, keepPlaceholder, keepIsOwn := keepInput(job.Retention, listing.Defaults.Retention)
	form := form{
		original:          original,
		passwordKept:      job.PasswordLiteral,
		originalRetention: job.Retention,
		known:             configedit.Known{Repositories: listing.Repositories, InheritsRepositories: len(listing.InheritedRepositories) > 0},
		defaults:          formDefaults{schedule: listing.Defaults.Schedule, repositories: listing.InheritedRepositories, retention: listing.Defaults.Retention},
		touched:           map[string]bool{},
	}
	port := ""
	if job.Port != 0 {
		port = strconv.Itoa(job.Port)
	}
	typeHint := "← → để chọn"
	if original != "" {
		typeHint = "không đổi được với job đã có"
	}
	passwordHint := "tên env var chứa mật khẩu (không ghi chính mật khẩu vào đây)"
	if job.PasswordLiteral {
		passwordHint = "file đang chứa mật khẩu ghi thẳng; nhập tên biến để thay, hoặc để trống để giữ nguyên"
	}
	scheduleHint := scheduleHint
	scheduleNote := "Chỉ chạy thủ công"
	if listing.Defaults.Schedule != "" {
		scheduleHint += "; để trống = schedule mặc định"
		scheduleNote = scheduleText(listing.Defaults.Schedule)
	}
	ticked := make([]bool, len(listing.Repositories))
	for i, name := range listing.Repositories {
		for _, own := range job.Repositories {
			ticked[i] = ticked[i] || own == name
		}
	}
	repositoriesHint := "space hoặc click để đánh dấu repository"
	if len(listing.InheritedRepositories) > 0 {
		repositoriesHint += "; không đánh dấu = " + strings.Join(listing.InheritedRepositories, ", ") + " (mặc định)"
	}
	storage := storageAuto
	if len(job.Repositories) > 0 {
		storage = storageRepository
	}
	retention := [5]string{}
	for i, count := range []int{job.Retention.Last, job.Retention.Daily, job.Retention.Weekly, job.Retention.Monthly, job.Retention.Yearly} {
		if count > 0 {
			retention[i] = strconv.Itoa(count)
		}
	}
	form.kind, form.title = formJob, "Thêm job"
	if original != "" {
		form.title = "Sửa " + original
	}
	form.fields = []formField{
		{key: "type", label: "Loại", hint: typeHint, kind: fieldChoice, value: job.Type, choices: configedit.JobTypes, fixed: original != "", section: sectionBasic},
		{key: "name", label: "Tên", hint: "chữ cái, chữ số, . _ -", value: job.Name, section: sectionBasic},
		{key: "paths", label: "Đường dẫn", hint: "file và thư mục cần backup, cách nhau bằng dấu phẩy; dùng được ${HOME}; Tab để hoàn thành, alt+1.. hoặc click để thêm thư mục gợi ý, ctrl+o để duyệt trên server", kind: fieldPaths, value: strings.Join(job.Paths, ", "), section: sectionBasic},
		{key: "database", label: "Database", hint: "database cần dump", value: job.Database, section: sectionBasic},
		{key: "host", label: "Host", hint: "mặc định localhost", value: job.Host, placeholder: "localhost", section: sectionConnection},
		{key: "port", label: "Cổng", hint: "mặc định 5432 / 3306 / 27017", kind: fieldNumber, value: port, placeholder: "mặc định", section: sectionConnection},
		{key: "user", label: "User", hint: "tuỳ chọn", value: job.User, section: sectionConnection},
		{key: "password_env", label: "Env var mật khẩu", hint: passwordHint, value: job.PasswordEnv, section: sectionConnection},
	}
	form.fields = append(form.fields, scheduleFields("schedule", "Schedule", scheduleHint, job.Schedule, scheduleNote, sectionSchedule)...)
	form.fields = append(form.fields, []formField{
		{key: "storage", label: "Storage", hint: "tự động: các repository mặc định, hoặc một thư mục riêng cho job · thư mục: chọn nơi lưu mới · repository: chọn repository đã có", kind: fieldChoice, value: storage, choices: []string{storageAuto, storageFolder, storageRepository}, section: sectionSchedule},
		{key: "store_at", label: "Thư mục", hint: "thư mục chứa backup của job này (tab để hoàn thành); sẽ tạo một repository cùng tên job cho nó", complete: true, placeholder: "nhập đường dẫn", section: sectionSchedule, onField: "storage", only: []string{storageFolder}},
		{key: "repositories", label: "Repositories", hint: repositoriesHint, kind: fieldChecklist, choices: listing.Repositories, ticked: ticked, section: sectionSchedule, onField: "storage", only: []string{storageRepository}},
		{key: "retention", label: "Giữ", hint: "số snapshot giữ lại: N bản gần nhất, rồi mỗi ngày, tuần, tháng, năm một bản; để trống hết = giữ tất cả", kind: fieldRetention, counts: retention, section: sectionSchedule},
		{key: "keep", label: "Giữ", hint: "giữ N bản mới nhất, bản cũ hơn tự xoá; để trống = quy tắc hiện có hoặc mặc định", kind: fieldNumber, value: keepValue, placeholder: keepPlaceholder, placeholderOwn: keepIsOwn, unit: "bản mới nhất", section: sectionSchedule},
	}...)
	form.fields = append(form.fields, []formField{
		{key: "advanced", label: "Tuỳ chọn nâng cao", hint: "enter hoặc space để mở hoặc đóng các tuỳ chọn bên dưới", kind: fieldToggle, section: sectionAdvanced},
		{key: "timeout", label: "Timeout", hint: "một lần backup được chạy tối đa bao lâu rồi bị cắt, vd. 12h, 36h. Để trống = 6h. Database lớn nên đặt rộng hơn thời gian backup thường lệ: backup qua stdin bị cắt thì phải làm lại từ đầu", value: job.Timeout, placeholder: "6h", section: sectionAdvanced},
		{key: "exclude", label: "Loại trừ", hint: "mẫu cần bỏ qua, cách nhau bằng dấu phẩy, ví dụ *.tmp, node_modules", value: strings.Join(job.Exclude, ", "), section: sectionAdvanced},
		{key: "tls", label: "TLS", hint: "chỉ với mysql: để trống, disabled hoặc required", value: job.TLS, section: sectionAdvanced},
		{key: "auth_database", label: "Auth database", hint: "chỉ với mongodb: database nơi user được tạo", value: job.AuthDatabase, section: sectionAdvanced},
	}...)
	form.syncSchedules()
	form.advancedOpen = len(job.Exclude) > 0 || job.Timeout != "" || job.TLS != "" || job.AuthDatabase != "" // keep what is set in view
	for i := range form.fields {
		if types, restricted := jobFieldTypes[form.fields[i].key]; restricted {
			form.fields[i].onField, form.fields[i].only = "type", types
		}
	}
	return form
}

// jobFieldTypes are the job types each type-specific field applies to; the others apply to all.
var jobFieldTypes = map[string][]string{
	"retention":     {config.JobFiles}, // a database job has the single "keep" input instead
	"keep":          configedit.DatabaseTypes,
	"paths":         {config.JobFiles},
	"exclude":       {config.JobFiles},
	"database":      configedit.DatabaseTypes,
	"host":          configedit.DatabaseTypes,
	"port":          configedit.DatabaseTypes,
	"user":          configedit.DatabaseTypes,
	"password_env":  configedit.DatabaseTypes,
	"tls":           {config.JobMySQL},
	"auth_database": {config.JobMongoDB},
}

// scheduleFields is a schedule field and the Time and Day rows that follow it. The rows show
// the parts of the schedule; they are only drawn when the schedule has them.
func scheduleFields(key, label, hint, value, placeholder, section string) []formField {
	return []formField{
		{key: key, label: label, hint: hint, kind: fieldSchedule, value: value, placeholder: placeholder, section: section},
		{key: key + "_time", label: "Time", hint: "giờ chạy, dạng 02:00 · ← → đổi 30 phút", kind: fieldTime, section: section},
		{key: key + "_day", label: "Day", hint: "← → đổi thứ trong tuần hoặc ngày trong tháng", kind: fieldDay, section: section},
	}
}

// syncSchedules refreshes the Time and Day rows from the schedule they belong to. A time that
// is half typed in the focused row is left alone.
func (f *form) syncSchedules() {
	for i := range f.fields {
		field := &f.fields[i]
		if field.kind != fieldTime && field.kind != fieldDay {
			continue
		}
		owner := f.field(strings.TrimSuffix(strings.TrimSuffix(field.key, "_time"), "_day"))
		plain, ok := schedule.Read(owner.value)
		switch {
		case !ok:
			field.value = ""
		case field.kind == fieldTime:
			if i == f.focus && !validClock(field.value) {
				continue
			}
			field.value = fmt.Sprintf("%02d:%02d", plain.Hour, plain.Minute)
		case plain.Kind == "weekly":
			field.value = schedule.WeekdayName(plain.Weekday)
		case plain.Kind == "monthly":
			field.value = "ngày " + strconv.Itoa(plain.Day)
		}
	}
}

// hasScheduleRow says whether the Time or Day row applies to the schedule it belongs to.
func (f form) hasScheduleRow(field formField) bool {
	owner := f.field(strings.TrimSuffix(strings.TrimSuffix(field.key, "_time"), "_day"))
	plain, ok := schedule.Read(owner.value)
	if !ok {
		return false
	}
	if field.kind == fieldTime {
		return plain.Kind == "daily" || plain.Kind == "weekly" || plain.Kind == "monthly"
	}
	return plain.Kind == "weekly" || plain.Kind == "monthly"
}

// validClock reports whether text is a time like 02:00 or 2:30.
func validClock(text string) bool {
	_, _, ok := parseClock(text)
	return ok
}

func parseClock(text string) (hour, minute int, ok bool) {
	left, right, found := strings.Cut(strings.TrimSpace(text), ":")
	if !found || len(right) != 2 || len(left) == 0 || len(left) > 2 {
		return 0, 0, false
	}
	hour, errHour := strconv.Atoi(left)
	minute, errMinute := strconv.Atoi(right)
	return hour, minute, errHour == nil && errMinute == nil && hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59
}

// visible are the fields that apply right now (to the chosen type or kind).
func (f form) visible() []int {
	var shown []int
	for i, field := range f.fields {
		if (field.kind == fieldTime || field.kind == fieldDay) && !f.hasScheduleRow(field) {
			continue
		}
		if field.onField != "" && !slices.Contains(field.only, f.field(field.onField).value) {
			continue
		}
		if field.section == sectionAdvanced && field.kind != fieldToggle && !f.advancedOpen {
			continue
		}
		shown = append(shown, i)
	}
	return shown
}

// focusOnField puts the focus on the field with that key, unfolding the advanced section for one in it. It
// says false, and leaves the focus where it is, when there is no such field or it is not on screen (a store_at
// of a job whose storage is automatic): a message under a field nobody can see would be no message.
func (f *form) focusOnField(key string) bool {
	index := slices.IndexFunc(f.fields, func(field formField) bool { return field.key == key })
	if key == "" || index < 0 {
		return false
	}
	if f.fields[index].section == sectionAdvanced {
		f.advancedOpen = true // the field with the problem must be in view
	}
	if !slices.Contains(f.visible(), index) {
		return false
	}
	f.focusOn(index)
	return true
}

// splitList splits a comma separated list, dropping blanks.
func splitList(text string) []string {
	var parts []string
	for _, part := range strings.Split(text, ",") {
		if part = strings.TrimSpace(part); part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func (f form) field(key string) *formField {
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
func (f form) job() (configedit.Job, error) {
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
		Timeout:      strings.TrimSpace(f.field("timeout").value),

		Paths:           splitList(f.field("paths").value),
		Exclude:         splitList(f.field("exclude").value),
		PasswordLiteral: f.passwordKept,
	}
	if portText := strings.TrimSpace(f.field("port").value); portText != "" {
		port, err := strconv.Atoi(portText)
		if err != nil {
			return job, formProblem("port", "cổng: %q không phải là số", portText)
		}
		job.Port = port
	}
	repositories := f.field("repositories")
	for i, name := range repositories.choices {
		if repositories.ticked[i] && f.field("storage").value == storageRepository {
			job.Repositories = append(job.Repositories, name)
		}
	}
	if f.databaseType() != "" {
		return f.withKeep(job)
	}
	counts := f.field("retention").counts
	for i, target := range []*int{&job.Retention.Last, &job.Retention.Daily, &job.Retention.Weekly, &job.Retention.Monthly, &job.Retention.Yearly} {
		if counts[i] == "" {
			continue
		}
		count, err := strconv.Atoi(counts[i])
		if err != nil {
			return job, formProblem("retention", "retention: %q không phải là số", counts[i])
		}
		*target = count
	}
	return job, nil
}

// keepInput is what the "keep at most N backups" input of a database job starts with. N when the
// job keeps only its last N; otherwise empty, with a placeholder that says what an empty input
// stands for: the default rule, or the job's own rule when it is more than a count of backups
// (own is true then, and the placeholder is a value of the job, not a default).
func keepInput(own, defaults config.Retention) (value, placeholder string, ownRule bool) {
	switch own {
	case config.Retention{}:
		return "", retentionText(defaults), false
	case config.Retention{Last: own.Last}:
		return strconv.Itoa(own.Last), "", false
	}
	return "", retentionText(own), true
}

// usesDefaults says the schedule, storage or retention of the job form fall back to the defaults
// of the config, so the section can say what the dim text means.
func (f form) usesDefaults() bool {
	if f.kind != formJob {
		return false
	}
	if strings.TrimSpace(f.field("schedule").value) == "" || f.field("storage").value == storageAuto {
		return true
	}
	if f.databaseType() != "" {
		keep := f.field("keep")
		return strings.TrimSpace(keep.value) == "" && !keep.placeholderOwn
	}
	return f.field("retention").counts == [5]string{}
}

// retentionText is a retention rule in the words of the screen: "7 ngày · 4 tuần", "Giữ tất cả".
func retentionText(retention config.Retention) string {
	var parts []string
	for i, count := range []int{retention.Last, retention.Daily, retention.Weekly, retention.Monthly, retention.Yearly} {
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, retentionUnits[i]))
		}
	}
	if len(parts) == 0 {
		return "Giữ tất cả"
	}
	return strings.Join(parts, " · ")
}

// scheduleText is a schedule in plain words with a capital: "Mỗi ngày lúc 02:00".
func scheduleText(spec string) string {
	return capitalize(schedule.Describe(spec))
}

// capitalize turns the first letter of text into a capital.
func capitalize(text string) string {
	first, size := utf8.DecodeRuneInString(text)
	return string(unicode.ToUpper(first)) + text[size:]
}

// withKeep sets the retention of a database job from its "keep" input: N backups, or what the job
// had when the form opened when the input is empty.
func (f form) withKeep(job configedit.Job) (configedit.Job, error) {
	text := strings.TrimSpace(f.field("keep").value)
	if text == "" {
		job.Retention = f.originalRetention
		return job, nil
	}
	count, err := strconv.Atoi(text)
	if err != nil || count < 1 {
		return job, formProblem("keep", "giữ ít nhất 1 bản; để trống = quy tắc hiện có hoặc mặc định")
	}
	job.Retention = config.Retention{Last: count}
	return job, nil
}

// problems is what is wrong with the form right now, by field key ("" for no field).
func (f form) problems() map[string]string {
	found := map[string]string{}
	add := func(err error) {
		var invalid *configedit.InvalidError
		if errors.As(err, &invalid) {
			if _, seen := found[invalid.Field]; !seen {
				found[invalid.Field] = invalid.Message
			}
		}
	}
	if f.kind != formJob {
		for _, err := range f.checkEntries() {
			add(err)
		}
		return found
	}
	job, known, _, err := f.jobWithStore()
	add(err)
	for _, problem := range configedit.Problems(&job, known) {
		add(problem)
	}
	return found
}

// shownProblems are the problems of the fields the user has been in (all after a failed save).
func (f form) shownProblems() map[string]string {
	shown := map[string]string{}
	for field, message := range f.problems() {
		if f.attempted || f.touched[field] {
			shown[field] = message
		}
	}
	return shown
}

// completes says whether Tab finishes a path in the field.
func (f formField) completes() bool { return f.kind == fieldPaths || f.complete }

// completeFocused finishes the path in the field on Tab. It reports whether it did anything
// (changed the text or listed candidates it had not listed yet), so Tab moves on otherwise.
func (f *form) completeFocused() bool {
	field := &f.fields[f.focus]
	if !field.completes() || field.fixed {
		return false
	}
	completed, options := completeList(field.value, field.kind == fieldPaths)
	listed := len(options) > 0 && !slices.Equal(options, field.options)
	changed := completed != field.value
	field.value, field.options, field.back = completed, options, 0
	f.touched[field.key] = true
	return changed || listed
}

// storeAt is the new folder typed in the "Store at" field of a job form, "" when it is empty.
func (f form) storeAt() string {
	if f.kind != formJob || f.field("storage").value != storageFolder {
		return ""
	}
	return strings.TrimSpace(f.field("store_at").value)
}

// jobWithStore is the job of the form plus the repository its "Store at" folder will create
// (named like the job), and the repositories the job may refer to once that one exists.
func (f form) jobWithStore() (configedit.Job, configedit.Known, configedit.Repository, error) {
	job, err := f.job()
	known := f.known
	folder := f.storeAt()
	if folder == "" {
		folder = f.autoFolder(job)
	}
	if folder == "" {
		return job, known, configedit.Repository{}, err
	}
	repo := configedit.Repository{Name: job.Name, URL: folder, PasswordEnv: "RESTIC_PASSWORD"}
	if slices.Contains(f.known.Repositories, repo.Name) {
		return job, known, repo, &configedit.InvalidError{Field: "store_at", Message: fmt.Sprintf("repository %q đã tồn tại: hãy đánh dấu nó ở dưới, hoặc đặt tên khác cho job", repo.Name)}
	}
	if problem := configedit.ValidateRepository(repo); problem != nil && job.Name != "" {
		var invalid *configedit.InvalidError
		if errors.As(problem, &invalid) && invalid.Field != "password_env" {
			return job, known, repo, &configedit.InvalidError{Field: "store_at", Message: invalid.Message}
		}
	}
	known.Repositories = append(slices.Clone(known.Repositories), repo.Name)
	job.Repositories = append(job.Repositories, repo.Name)
	return job, known, repo, err
}

// autoStoreBase is the folder under which Sentinel puts the repository of a database job that
// was given no place to store its backups: one folder per job.
func autoStoreBase() string {
	if os.Geteuid() == 0 {
		return "/var/backups/sentinel"
	}
	return "${HOME}/.local/share/sentinel"
}

// autoFolder is the folder Sentinel builds for a job whose Storage is automatic and that has no
// repository from the defaults: <base>/<job name>. It is "" for any other job.
func (f form) autoFolder(job configedit.Job) string {
	if f.kind != formJob || job.Name == "" || f.field("storage").value != storageAuto ||
		len(job.Repositories) > 0 || f.known.InheritsRepositories ||
		configedit.ValidateRepository(configedit.Repository{Name: job.Name, URL: "x", PasswordEnv: "X"}) != nil { // a bad name is the name field's problem
		return ""
	}
	return autoStoreBase() + "/" + job.Name
}
