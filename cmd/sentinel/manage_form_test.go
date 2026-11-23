package main

import (
	"errors"
	"strings"
	"testing"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
)

type fakeConfig struct {
	listing   configedit.Listing
	saved     []configedit.Job
	savedFrom []string
	deleted   []string
	saveErr   error
}

func (f *fakeConfig) attach(actions *manageActions) {
	actions.listJobs = func() (configedit.Listing, error) { return f.listing, nil }
	actions.saveJob = func(original string, job configedit.Job) error {
		f.savedFrom = append(f.savedFrom, original)
		f.saved = append(f.saved, job)
		return f.saveErr
	}
	actions.deleteJob = func(name string) error {
		f.deleted = append(f.deleted, name)
		return nil
	}
}

func formModel(t *testing.T) (manageModel, *fakeConfig) {
	t.Helper()
	configFile := &fakeConfig{listing: configedit.Listing{
		Repositories: []string{"local", "s3"},
		Jobs: []configedit.Job{
			{Name: "appdb", Type: config.BackupPostgres, Database: "app", Host: "db", Port: 5433, PasswordLiteral: true, Repositories: []string{"s3"}, Retention: config.Retention{Daily: 7}},
			{Name: "files", Type: config.BackupFiles, Repositories: []string{"local"}},
		},
	}}
	actions := (&fakeActions{}).actions()
	configFile.attach(&actions)
	screen := newManageModel(t.Context(), func() (statusReport, error) { return sampleReport(), nil }, actions, jobRepositoriesOf(configFile.listing))
	model, _ := screen.Update(reportMsg{report: sampleReport()})
	return model.(manageModel), configFile
}

func fillField(t *testing.T, screen manageModel, text string) manageModel {
	t.Helper()
	screen = typeText(t, screen, text)
	model, _ := screen.onKey("tab", "")
	return model.(manageModel)
}

func TestAddDatabaseJobThroughTheForm(t *testing.T) {
	screen, configFile := formModel(t)
	model, _ := screen.onKey("a", "")
	screen = model.(manageModel)
	if screen.mode != modeForm || !strings.Contains(plain(screen.render()), "Add a database job") {
		t.Fatalf("form missing (mode %d):\n%s", screen.mode, plain(screen.render()))
	}
	model, _ = screen.onKey("right", "") // type: postgres -> mysql
	screen = model.(manageModel)
	if screen.form.field("type").value != config.BackupMySQL || !strings.Contains(plain(screen.render()), "TLS") || strings.Contains(plain(screen.render()), "Auth database") {
		t.Fatalf("type-specific fields wrong:\n%s", plain(screen.render()))
	}
	model, _ = screen.onKey("tab", "") // -> name
	screen = model.(manageModel)
	for _, input := range []string{"shop", "shopdb", "", "", "dump", "MYSQL_PW", "30 2 * * *", "s3, local", "daily=7 weekly=4"} {
		screen = fillField(t, screen, input)
	}
	screen = typeText(t, screen, "disabled") // TLS, the last visible field
	model, _ = screen.onKey("enter", "")
	screen = model.(manageModel)
	if len(configFile.saved) != 1 {
		t.Fatalf("not saved (mode %d): %s", screen.mode, screen.form.problem)
	}
	got := configFile.saved[0]
	if configFile.savedFrom[0] != "" || got.Name != "shop" || got.Type != config.BackupMySQL || got.Database != "shopdb" || got.User != "dump" ||
		got.PasswordEnv != "MYSQL_PW" || got.Schedule != "30 2 * * *" || strings.Join(got.Repositories, ",") != "s3,local" ||
		got.Retention.Daily != 7 || got.Retention.Weekly != 4 || got.TLS != "disabled" {
		t.Errorf("job saved = %+v", got)
	}
	if screen.mode != modeTable || !strings.Contains(screen.notice, "Added job shop") || !strings.Contains(screen.notice, "Restart the daemon") {
		t.Errorf("notice = %q (mode %d)", screen.notice, screen.mode)
	}
}

func TestEditFormIsPrefilledKeepsTheTypeFixedAndExplainsLiteralPasswords(t *testing.T) {
	screen, configFile := formModel(t)
	model, _ := screen.onKey("e", "")
	screen = model.(manageModel)
	view := plain(screen.render())
	for _, want := range []string{"Edit appdb", "appdb", "5433", "daily=7", "s3"} {
		if !strings.Contains(view, want) {
			t.Errorf("form lacks %q:\n%s", want, view)
		}
	}
	model, _ = screen.onKey("right", "")
	if model.(manageModel).form.field("type").value != config.BackupPostgres {
		t.Error("the type of an existing job cannot be cycled")
	}
	screen = model.(manageModel)
	for range 6 { // move to the password env field
		model, _ = screen.onKey("tab", "")
		screen = model.(manageModel)
	}
	if !strings.Contains(plain(screen.render()), "literal password") {
		t.Errorf("the literal-password hint is missing:\n%s", plain(screen.render()))
	}
	screen.onKey("ctrl+s", "")
	if len(configFile.saved) != 1 || configFile.savedFrom[0] != "appdb" || !configFile.saved[0].PasswordLiteral || configFile.saved[0].PasswordEnv != "" {
		t.Errorf("saved = %+v from %v (an untouched literal password must be kept)", configFile.saved, configFile.savedFrom)
	}
}

func TestFormShowsValidationProblemsAndStaysOpen(t *testing.T) {
	screen, configFile := formModel(t)
	configFile.saveErr = &configedit.InvalidError{Message: "database name is required"}
	model, _ := screen.onKey("a", "")
	model, _ = model.(manageModel).onKey("ctrl+s", "")
	screen = model.(manageModel)
	if screen.mode != modeForm || !strings.Contains(plain(screen.render()), "database name is required") {
		t.Errorf("want the problem in the form (mode %d):\n%s", screen.mode, plain(screen.render()))
	}

	screen.form.field("port").value = "abc"
	model, _ = screen.onKey("ctrl+s", "")
	screen = model.(manageModel)
	if !strings.Contains(screen.form.problem, "not a number") || len(configFile.saved) != 1 {
		t.Errorf("port problem = %q, saves = %d (a bad number must not reach the save)", screen.form.problem, len(configFile.saved))
	}
	screen.form.field("port").value = ""
	screen.form.field("retention").value = "daily=x"
	model, _ = screen.onKey("ctrl+s", "")
	if !strings.Contains(model.(manageModel).form.problem, "retention") {
		t.Errorf("retention problem = %q", model.(manageModel).form.problem)
	}
}

func TestUnexpectedSaveFailureLeavesTheFormWithANotice(t *testing.T) {
	screen, configFile := formModel(t)
	configFile.saveErr = errors.New("permission denied")
	model, _ := screen.onKey("a", "")
	screen = typeText(t, model.(manageModel), "")
	screen.form.field("name").value, screen.form.field("database").value, screen.form.field("repositories").value = "x", "x", "s3"
	model, _ = screen.onKey("ctrl+s", "")
	screen = model.(manageModel)
	if screen.mode != modeTable || !screen.noticeBad || !strings.Contains(screen.notice, "permission denied") {
		t.Errorf("mode %d notice %q", screen.mode, screen.notice)
	}
}

func TestFilesJobsCannotBeEditedOrRemovedHere(t *testing.T) {
	screen, configFile := formModel(t)
	screen.cursor = 1 // the files line of the report ("files" job failed run)
	model, _ := screen.onKey("e", "")
	screen = model.(manageModel)
	if screen.mode != modeTable || !strings.Contains(screen.notice, "only database jobs") {
		t.Errorf("edit files job: mode %d notice %q", screen.mode, screen.notice)
	}
	model, _ = screen.onKey("d", "")
	screen = model.(manageModel)
	if screen.mode != modeTable || !strings.Contains(screen.notice, "only those can be removed") || len(configFile.deleted) != 0 {
		t.Errorf("delete files job: mode %d notice %q", screen.mode, screen.notice)
	}
}

func TestRemoveNeedsTheJobName(t *testing.T) {
	screen, configFile := formModel(t)
	model, _ := screen.onKey("d", "")
	screen = model.(manageModel)
	if screen.mode != modeDelete || !strings.Contains(plain(screen.render()), "Remove appdb") {
		t.Fatalf("delete screen missing (mode %d):\n%s", screen.mode, plain(screen.render()))
	}
	screen = typeText(t, screen, "nope")
	model, _ = screen.onKey("enter", "")
	screen = model.(manageModel)
	if len(configFile.deleted) != 0 || screen.inputError == "" {
		t.Fatal("a wrong name must not remove the job")
	}
	for range "nope" {
		model, _ = screen.onKey("backspace", "")
		screen = model.(manageModel)
	}
	screen = typeText(t, screen, "appdb")
	model, _ = screen.onKey("enter", "")
	screen = model.(manageModel)
	if len(configFile.deleted) != 1 || configFile.deleted[0] != "appdb" || screen.mode != modeTable || !strings.Contains(screen.notice, "snapshots stay") {
		t.Errorf("deleted %v mode %d notice %q", configFile.deleted, screen.mode, screen.notice)
	}
}

func TestFormKeysOnOtherTabsDoNothing(t *testing.T) {
	screen, _ := formModel(t)
	model, _ := screen.onKey("2", "")
	model, _ = model.(manageModel).onKey("a", "")
	if model.(manageModel).mode != modeTable {
		t.Error("a on the Uptime tab must not open the form")
	}
}

func TestParseRetention(t *testing.T) {
	got, err := parseRetention("daily=7, weekly=4 last=2")
	if err != nil || got.Daily != 7 || got.Weekly != 4 || got.Last != 2 {
		t.Errorf("got %+v, %v", got, err)
	}
	for _, bad := range []string{"daily", "daily=x", "daily=-1", "hourly=3"} {
		if _, err := parseRetention(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}
