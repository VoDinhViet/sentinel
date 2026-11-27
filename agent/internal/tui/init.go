package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/configedit"
	"sentinel/agent/internal/notify"
	"sentinel/agent/internal/status"
)

// InitOutcome is what a finished init tells the user.
type InitOutcome struct {
	Path, EnvFile string
	PasswordEnv   string
	SecretWritten bool
	JobName       string
	RepoName      string
	NotifierKind  string   // the alert channel set up, "" for none
	NotifierVars  []string // the variables of that channel the user still has to set
}

// RunInit shows the form and writes the config; it returns nil when the user cancelled.
func RunInit(ctx context.Context, path string, root, force bool) (*InitOutcome, error) {
	hostname, _ := os.Hostname()
	outcome := &InitOutcome{Path: path}
	actions := manageActions{initialize: func(initial configedit.Initial) error {
		if err := configedit.Create(path, initial, force); err != nil {
			return err
		}
		outcome.JobName, outcome.RepoName, outcome.PasswordEnv = initial.Job.Name, initial.Repository.Name, initial.Repository.PasswordEnv
		if notifier := initial.Notifier; notifier != nil {
			outcome.NotifierKind = notifier.Kind
			for _, spec := range notify.FieldsOf(notifier.Kind) {
				if spec.Secret {
					outcome.NotifierVars = append(outcome.NotifierVars, notifier.Values[spec.Key])
				}
			}
		}
		// A server install keeps secrets in sentinel.env beside the config: give the repository a password there.
		if root && filepath.Dir(path) == filepath.Dir(config.SystemPath) {
			outcome.EnvFile = filepath.Join(filepath.Dir(path), "sentinel.env")
			written, err := configedit.EnsureSecret(outcome.EnvFile, initial.Repository.PasswordEnv)
			if err != nil {
				return fmt.Errorf("đã ghi config, nhưng chưa ghi được mật khẩu repository: %w", err)
			}
			outcome.SecretWritten = written
		}
		return nil
	}}
	model := newManageModel(ctx, func() (status.Report, error) { return status.Report{Server: hostname}, nil }, actions, nil)
	model.loaded = true
	model.report.Server = hostname
	model = model.openEntryForm(newInitForm(path, root, hostname))
	final, err := tea.NewProgram(initProgram{manageModel: model}, tea.WithContext(ctx)).Run()
	if err != nil {
		return nil, err
	}
	program := final.(initProgram)
	switch {
	case program.noticeBad:
		return nil, errors.New(program.notice)
	case program.saved:
		return outcome, nil
	}
	return nil, nil
}

// initProgram is the manage screen reduced to one form: it ends when the form is saved or cancelled.
type initProgram struct {
	manageModel
	saved bool
}

func (p initProgram) Init() tea.Cmd { return tea.RequestBackgroundColor }

func (p initProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return p.after(p.manageModel.Update(msg))
}

// after takes what the screen did and ends the program once the form is gone.
func (p initProgram) after(model tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	p.manageModel = model.(manageModel)
	if p.onTabs() { // the form and the folder browser are screens over the tabs
		p.saved = !p.noticeBad && p.notice != ""
		return p, tea.Quit
	}
	return p, cmd
}

// ---- the form

var initJobTypes = []string{config.JobFiles, config.JobPostgres, config.JobMySQL, config.JobMongoDB}

// The alert channels the init form can set up; "không" writes none.
const initNotifierNone = "không"

var initNotifierKinds = []string{initNotifierNone, "telegram", "discord", "slack"}

// initNotifier is the alert channel the init form describes, or nil for "không".
func (f form) initNotifier() *configedit.Notifier {
	kind := f.field("notify_kind").value
	if kind == initNotifierNone || kind == "" {
		return nil
	}
	notifier := configedit.Notifier{Kind: kind, Values: map[string]string{}, Literal: map[string]bool{}}
	for _, spec := range notify.FieldsOf(kind) {
		notifier.Values[spec.Key] = strings.TrimSpace(f.field("notify_" + kind + "." + spec.Key).value)
	}
	return &notifier
}

// newInitForm is the repository form plus the first job and its schedule, on one screen.
func newInitForm(path string, root bool, hostname string) form {
	repoLocation, jobPaths, store := "${HOME}/.local/share/sentinel/repo", "${HOME}/Documents", "${HOME}/.local/state/sentinel/state.db"
	if root {
		repoLocation, jobPaths, store = "/var/backups/sentinel", "/etc, /var/www", "" // the default, /var/lib/sentinel/state.db
	}
	repo := newRepositoryForm(configedit.Repository{
		Name: "local", URL: repoLocation, PasswordEnv: "RESTIC_PASSWORD",
		Env: map[string]string{awsKeyID: awsKeyID, awsSecretKey: awsSecretKey},
	}, "", configedit.Listing{})
	jobTypeOnly := func(types ...string) (string, []string) { return "job_type", types }
	jobField := func(key, label, hint, value string, types ...string) formField {
		field := formField{key: key, label: label, hint: hint, value: value}
		if len(types) > 0 {
			field.onField, field.only = jobTypeOnly(types...)
		}
		return field
	}
	fields := append([]formField{
		{key: "server_name", label: "Tên server", hint: "hiện trong cảnh báo và trên màn hình", value: hostname},
		{key: "store", label: "File trạng thái", hint: "nơi lưu lịch sử chạy và khoá job (đường dẫn file)", value: store, placeholder: "/var/lib/sentinel/state.db"},
	}, repo.fields...)
	for i := range fields {
		switch fields[i].key {
		case "name":
			fields[i].label, fields[i].hint = "Tên repository", "chữ, số, . _ - ; repository backup đầu tiên"
		case "kind":
			fields[i].label, fields[i].hint = "Nơi lưu backup", "← → chọn nơi lưu backup; có thể thêm repository khác sau"
		}
	}
	fields = append(fields,
		formField{key: "job_type", label: "Backup gì", hint: "← → file và thư mục, hoặc dump database", kind: fieldChoice, value: config.JobFiles, choices: initJobTypes},
		jobField("job_name", "Tên job", "chữ, số, . _ -", "documents"),
		jobField("job_paths", "Đường dẫn", "file và thư mục cần backup, cách nhau bằng dấu phẩy; dùng được ${HOME}; alt+1.. hoặc click để thêm thư mục gợi ý, ctrl+o để duyệt trên server", jobPaths, config.JobFiles),
		jobField("job_database", "Database", "database cần dump", "", configedit.DatabaseTypes...),
		jobField("job_password_env", "Env var mật khẩu DB", "tên env var chứa mật khẩu database; để trống nếu không có", "", configedit.DatabaseTypes...),
	)
	fields = append(fields, scheduleFields("job_schedule", "Schedule", scheduleHint, "daily 02:00", "", "")...)
	fields = append(fields,
		formField{key: "retention", label: "Giữ", hint: "số snapshot giữ lại: N cái mới nhất, rồi mỗi ngày, tuần, tháng, năm một cái", kind: fieldRetention, counts: [5]string{"", "7", "4", "6", ""}},
		formField{key: "notify_kind", label: "Cảnh báo qua", hint: "← → chọn kênh nhận cảnh báo (backup lỗi, uptime down); không = chỉ ghi log. Muốn SMTP hay kênh thứ hai thì thêm sau trong sentinel manage", kind: fieldChoice, value: initNotifierNone, choices: initNotifierKinds},
		formField{key: "notify_telegram.token", label: "Token var", hint: "tên env var chứa bot token từ @BotFather (không nhập chính token); đặt giá trị trong sentinel.env", value: "TG_TOKEN", onField: "notify_kind", only: []string{"telegram"}},
		formField{key: "notify_telegram.chat_id", label: "Chat id", hint: "id của chat hoặc group; id group bắt đầu bằng -", onField: "notify_kind", only: []string{"telegram"}},
		formField{key: "notify_discord.webhook", label: "Webhook var", hint: "tên env var chứa webhook URL của Discord", value: "DISCORD_WEBHOOK", onField: "notify_kind", only: []string{"discord"}},
		formField{key: "notify_slack.webhook", label: "Webhook var", hint: "tên env var chứa webhook URL của Slack", value: "SLACK_WEBHOOK", onField: "notify_kind", only: []string{"slack"}},
	)
	for i := range fields {
		if fields[i].key == "job_paths" {
			fields[i].kind = fieldPaths
		}
		if fields[i].key == "job_database" || fields[i].key == "job_password_env" {
			fields[i].onField, fields[i].only = "job_type", configedit.DatabaseTypes
		}
	}
	initForm := form{kind: formInit, title: "Tạo " + path, touched: map[string]bool{}, fields: fields}
	initForm.syncSchedules()
	return initForm
}

// initial turns the form into what configedit.Create needs.
func (f form) initial() (configedit.Initial, error) {
	repo := f.repository()
	job := configedit.Job{
		Name: strings.TrimSpace(f.field("job_name").value), Type: f.field("job_type").value,
		Paths: splitList(f.field("job_paths").value), Database: strings.TrimSpace(f.field("job_database").value),
		PasswordEnv: strings.TrimSpace(f.field("job_password_env").value),
	}
	if job.Type != config.JobFiles {
		job.Paths = nil
	}
	var retention config.Retention
	for i, target := range []*int{&retention.Last, &retention.Daily, &retention.Weekly, &retention.Monthly, &retention.Yearly} {
		if text := f.field("retention").counts[i]; text != "" {
			if _, err := fmt.Sscan(text, target); err != nil {
				return configedit.Initial{}, formProblem("retention", "retention: %q không phải là số", text)
			}
		}
	}
	return configedit.Initial{
		ServerName: strings.TrimSpace(f.field("server_name").value), Store: strings.TrimSpace(f.field("store").value), Repository: repo, Job: job,
		Schedule: strings.TrimSpace(f.field("job_schedule").value), Retention: retention, Notifier: f.initNotifier(),
	}, nil
}

// initProblems validates the init form without writing anything.
func (f form) initProblems() []error {
	initial, err := f.initial()
	if err != nil {
		return []error{err}
	}
	problems := f.repositoryProblems(initial.Repository)
	jobKeys := map[string]string{"name": "job_name", "type": "job_type", "paths": "job_paths", "database": "job_database", "password_env": "job_password_env", "schedule": "job_schedule"}
	job := initial.Job
	job.Schedule = initial.Schedule
	known := configedit.Known{Repositories: []string{initial.Repository.Name}, InheritsRepositories: true}
	for _, problem := range configedit.Problems(&job, known) {
		key := jobKeys[problem.Field]
		if key == "" {
			key = problem.Field
		}
		problems = append(problems, formProblem(key, "%s", problem.Message))
	}
	if notifier := initial.Notifier; notifier != nil {
		if err := configedit.ValidateNotifier(*notifier); err != nil {
			var invalid *configedit.InvalidError
			if errors.As(err, &invalid) {
				problems = append(problems, formProblem("notify_"+notifier.Kind+"."+invalid.Field, "%s", invalid.Message))
			}
		}
	}
	return problems
}
