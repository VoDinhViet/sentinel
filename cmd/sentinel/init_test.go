package main

import (
	"strings"
	"testing"

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
	for _, want := range []string{"Tạo /home/me/.config/sentinel/config.yaml", "Tên repository", "local", "Nơi lưu backup", "${HOME}/.local/share/sentinel/repo", "RESTIC_PASSWORD", "Backup gì", "documents", "${HOME}/Documents", "daily 02:00"} {
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
	if !strings.Contains(plain(program.render()), "✗ database name is required") {
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
	for _, want := range []string{"✗ invalid repository name", "✗ cần nhập thư mục", "✗ invalid schedule", "✗ invalid job name"} {
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

func TestPrintInitOutcomeTellsWhatToDoNext(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "")
	var output strings.Builder
	printInitOutcome(&output, &initOutcome{path: "/home/me/.config/sentinel/config.yaml", passwordEnv: "RESTIC_PASSWORD", jobName: "documents"})
	for _, want := range []string{"Đã ghi /home/me/.config/sentinel/config.yaml", "sẽ hỏi RESTIC_PASSWORD", "sentinel backup init documents", "sentinel doctor", "sentinel manage"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, output.String())
		}
	}
	output.Reset()
	printInitOutcome(&output, &initOutcome{path: "/etc/sentinel/config.yaml", envFile: "/etc/sentinel/sentinel.env", passwordEnv: "RESTIC_PASSWORD", secretWritten: true, jobName: "documents"})
	if !strings.Contains(output.String(), "mật khẩu repository ngẫu nhiên") || !strings.Contains(output.String(), ". /etc/sentinel/sentinel.env") || strings.Contains(output.String(), "export RESTIC_PASSWORD") {
		t.Errorf("root output:\n%s", output.String())
	}
}
