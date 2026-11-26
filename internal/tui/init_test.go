package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
	"sentinel/internal/status"
)

// initScreen is the init form hosted the way `sentinel init` hosts it, with a fake writer.
func initScreen(t *testing.T, root bool) (initProgram, *[]configedit.Initial) {
	t.Helper()
	var written []configedit.Initial
	actions := manageActions{initialize: func(initial configedit.Initial) error {
		written = append(written, initial)
		return nil
	}}
	model := newManageModel(t.Context(), func() (status.Report, error) { return status.Report{Server: "web-01"}, nil }, actions, nil)
	model.loaded = true
	return initProgram{manageModel: model.openEntryForm(newInitForm("/home/me/.config/sentinel/config.yaml", root, "web-01"))}, &written
}

func pressInit(t *testing.T, program initProgram, key string) initProgram {
	t.Helper()
	model, _ := program.after(program.onKey(key, ""))
	return model.(initProgram)
}

func TestInitFormStartsValidAndWritesTheDefaults(t *testing.T) {
	program, written := initScreen(t, false)
	view := plain(program.render())
	for _, want := range []string{"Tạo /home/me/.config/sentinel/config.yaml", "Tên repository", "local", "Nơi lưu backup", "${HOME}/.local/share/sentinel/repo", "RESTIC_PASSWORD", "Backup gì", "documents", "${HOME}/Documents", "[ Daily ▾ ]", "[ 02:00"} {
		if !strings.Contains(view, want) {
			t.Errorf("init form lacks %q:\n%s", want, view)
		}
	}
	if problems := program.form.problems(); len(problems) != 0 {
		t.Fatalf("the defaults must be valid: %v", problems)
	}
	program = pressInit(t, program, "ctrl+s")
	if !program.saved || len(*written) != 1 {
		t.Fatalf("saved %v, written %d (notice %q)", program.saved, len(*written), program.notice)
	}
	got := (*written)[0]
	if got.Store != "${HOME}/.local/state/sentinel/state.db" {
		t.Errorf("a user's state database lives under their home, not in /var/lib: %q", got.Store)
	}
	if got.ServerName != "web-01" || got.Repository.Name != "local" || got.Repository.URL != "${HOME}/.local/share/sentinel/repo" || got.Repository.PasswordEnv != "RESTIC_PASSWORD" ||
		got.Job.Name != "documents" || got.Job.Type != config.JobFiles || strings.Join(got.Job.Paths, "|") != "${HOME}/Documents" ||
		got.Schedule != "daily 02:00" || got.Retention != (config.Retention{Daily: 7, Weekly: 4, Monthly: 6}) {
		t.Errorf("initial = %+v", got)
	}
}

func TestInitFormAsRootUsesServerPaths(t *testing.T) {
	program, _ := initScreen(t, true)
	if program.form.field("store").value != "" {
		t.Errorf("root keeps the default state database: %q", program.form.field("store").value)
	}
	if program.form.field("path").value != "/var/backups/sentinel" || program.form.field("job_paths").value != "/etc, /var/www" {
		t.Errorf("root defaults: %q %q", program.form.field("path").value, program.form.field("job_paths").value)
	}
}

func TestInitFormAsksForADatabaseWhenTheJobIsOne(t *testing.T) {
	program, written := initScreen(t, false)
	if strings.Contains(plain(program.render()), "Database") {
		t.Error("a files job has no database field")
	}
	program.form.field("job_type").value = config.JobPostgres
	view := plain(program.render())
	if !strings.Contains(view, "Env var mật khẩu DB") || strings.Contains(view, "Đường dẫn") {
		t.Errorf("postgres fields:\n%s", view)
	}
	program = pressInit(t, program, "ctrl+s")
	if program.saved || len(*written) != 0 || program.form.fields[program.form.focus].key != "job_database" {
		t.Fatalf("a database job needs a database (saved %v, focus %q)", program.saved, program.form.fields[program.form.focus].key)
	}
	if !strings.Contains(plain(program.render()), "✗ cần nhập tên database") {
		t.Errorf("problem under the field:\n%s", plain(program.render()))
	}
	program.form.field("job_database").value = "app"
	program = pressInit(t, program, "ctrl+s")
	if !program.saved || (*written)[0].Job.Database != "app" || len((*written)[0].Job.Paths) != 0 {
		t.Errorf("written = %+v", *written)
	}
}

func TestInitFormShowsEveryProblemOnItsOwnField(t *testing.T) {
	program, _ := initScreen(t, false)
	program.form.field("name").value = "bad name"
	program.form.field("path").value = ""
	program.form.field("job_schedule").value = "daily 25:00"
	program.form.field("job_name").value = ""
	program = pressInit(t, program, "ctrl+s")
	view := plain(program.render())
	for _, want := range []string{"✗ tên repository", "✗ cần nhập thư mục", "✗ schedule không hợp lệ", "✗ tên job"} {
		if !strings.Contains(view, want) {
			t.Errorf("init form lacks %q:\n%s", want, view)
		}
	}
}

func TestInitFormCancelWritesNothing(t *testing.T) {
	program, written := initScreen(t, false)
	program = pressInit(t, program, "esc")
	if program.saved || len(*written) != 0 || program.mode == modeForm {
		t.Errorf("saved %v, written %d, mode %d", program.saved, len(*written), program.mode)
	}
}

func TestInitFormAlertChannelIsOptional(t *testing.T) {
	program, written := initScreen(t, false)
	if program.form.field("notify_kind").value != initNotifierNone || len(program.form.problems()) != 0 {
		t.Fatalf("by default no channel is asked for and the form is valid: %v", program.form.problems())
	}
	if view := plain(program.render()); !strings.Contains(view, "Cảnh báo qua") || strings.Contains(view, "Token var") {
		t.Errorf("the channel field shows, its fields do not:\n%s", view)
	}
	program = pressInit(t, program, "ctrl+s")
	if !program.saved || (*written)[0].Notifier != nil {
		t.Errorf("without a channel nothing is written for it: %+v", *written)
	}

	program, written = initScreen(t, false)
	program.form.field("notify_kind").value = "telegram"
	if view := plain(program.render()); !strings.Contains(view, "Token var") || !strings.Contains(view, "Chat id") {
		t.Errorf("telegram asks for its two fields:\n%s", view)
	}
	program = pressInit(t, program, "ctrl+s")
	if program.saved || len(*written) != 0 || program.form.problems()["notify_telegram.chat_id"] == "" {
		t.Fatalf("telegram without a chat id is refused (saved %v): %v", program.saved, program.form.problems())
	}
	program.form.field("notify_telegram.chat_id").value = "-1001234"
	program = pressInit(t, program, "ctrl+s")
	if !program.saved || len(*written) != 1 {
		t.Fatalf("not saved: %v", program.form.problems())
	}
	notifier := (*written)[0].Notifier
	if notifier == nil || notifier.Kind != "telegram" || notifier.Values["token"] != "TG_TOKEN" || notifier.Values["chat_id"] != "-1001234" {
		t.Errorf("notifier = %+v", notifier)
	}
}

func TestInitFormConnectTestsTheFirstDatabaseJob(t *testing.T) {
	program, _ := initScreen(t, false)
	var tested []configedit.Job
	program.actions.testConnection = func(_ context.Context, job configedit.Job, _ string) (time.Duration, error) {
		tested = append(tested, job)
		return time.Millisecond, nil
	}
	if view := plain(program.render()); strings.Contains(view, "Kết nối thử") {
		t.Fatalf("a files job has nothing to connect to:\n%s", view)
	}
	program.form.field("job_type").value = config.JobPostgres
	program.form.field("job_database").value = "app"
	program.form.field("job_password_env").value = "APP_PW"
	if view := plain(program.render()); !strings.Contains(view, "[ Kết nối thử ]") {
		t.Fatalf("a database job offers it:\n%s", view)
	}
	model, cmd := program.onKey("ctrl+t", "")
	program.manageModel = feed(t, model.(manageModel), messagesOf(cmd))
	if len(tested) != 1 || tested[0].Type != config.JobPostgres || tested[0].Database != "app" || tested[0].PasswordEnv != "APP_PW" {
		t.Errorf("tested %+v", tested)
	}
	if view := plain(program.render()); !strings.Contains(view, "✓ kết nối được") {
		t.Errorf("the result shows:\n%s", view)
	}
}
