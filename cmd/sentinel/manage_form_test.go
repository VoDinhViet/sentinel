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

func formManager(t *testing.T) (manager, *fakeConfig) {
	t.Helper()
	fakeCfg := &fakeConfig{listing: configedit.Listing{
		Repositories: []string{"local", "s3"},
		Jobs: []configedit.Job{
			{Name: "appdb", Type: config.BackupPostgres, Database: "app", Host: "db", Port: 5433, PasswordLiteral: true, Repositories: []string{"s3"}, Retention: config.Retention{Daily: 7}},
			{Name: "files", Type: config.BackupFiles, Repositories: []string{"local"}},
		},
	}}
	actions := (&fakeActions{}).actions()
	fakeCfg.attach(&actions)
	d := newManager(t.Context(), func() (statusReport, error) { return sampleReport(), nil }, actions, pairsOf(fakeCfg.listing))
	model, _ := d.Update(reportMsg{report: sampleReport()})
	return model.(manager), fakeCfg
}

func fillField(t *testing.T, d manager, text string) manager {
	t.Helper()
	d = typeText(t, d, text)
	model, _ := d.onKey("tab", "")
	return model.(manager)
}

func TestAddDatabaseJobThroughTheForm(t *testing.T) {
	d, fakeCfg := formManager(t)
	model, _ := d.onKey("a", "")
	d = model.(manager)
	if d.mode != modeForm || !strings.Contains(plain(d.render()), "Add a database job") {
		t.Fatalf("form missing (mode %d):\n%s", d.mode, plain(d.render()))
	}
	model, _ = d.onKey("right", "") // type: postgres -> mysql
	d = model.(manager)
	if d.form.field("type").value != config.BackupMySQL || !strings.Contains(plain(d.render()), "TLS") || strings.Contains(plain(d.render()), "Auth database") {
		t.Fatalf("type-specific fields wrong:\n%s", plain(d.render()))
	}
	model, _ = d.onKey("tab", "") // -> name
	d = model.(manager)
	for _, input := range []string{"shop", "shopdb", "", "", "dump", "MYSQL_PW", "30 2 * * *", "s3, local", "daily=7 weekly=4"} {
		d = fillField(t, d, input)
	}
	d = typeText(t, d, "disabled") // TLS, the last visible field
	model, _ = d.onKey("enter", "")
	d = model.(manager)
	if len(fakeCfg.saved) != 1 {
		t.Fatalf("not saved (mode %d): %s", d.mode, d.form.problem)
	}
	got := fakeCfg.saved[0]
	if fakeCfg.savedFrom[0] != "" || got.Name != "shop" || got.Type != config.BackupMySQL || got.Database != "shopdb" || got.User != "dump" ||
		got.PasswordEnv != "MYSQL_PW" || got.Schedule != "30 2 * * *" || strings.Join(got.Repositories, ",") != "s3,local" ||
		got.Retention.Daily != 7 || got.Retention.Weekly != 4 || got.TLS != "disabled" {
		t.Errorf("job saved = %+v", got)
	}
	if d.mode != modeTable || !strings.Contains(d.notice, "Added job shop") || !strings.Contains(d.notice, "Restart the daemon") {
		t.Errorf("notice = %q (mode %d)", d.notice, d.mode)
	}
}

func TestEditFormIsPrefilledKeepsTheTypeFixedAndExplainsLiteralPasswords(t *testing.T) {
	d, fakeCfg := formManager(t)
	model, _ := d.onKey("e", "")
	d = model.(manager)
	view := plain(d.render())
	for _, want := range []string{"Edit appdb", "appdb", "5433", "daily=7", "s3"} {
		if !strings.Contains(view, want) {
			t.Errorf("form lacks %q:\n%s", want, view)
		}
	}
	model, _ = d.onKey("right", "")
	if model.(manager).form.field("type").value != config.BackupPostgres {
		t.Error("the type of an existing job cannot be cycled")
	}
	d = model.(manager)
	for range 6 { // move to the password env field
		model, _ = d.onKey("tab", "")
		d = model.(manager)
	}
	if !strings.Contains(plain(d.render()), "literal password") {
		t.Errorf("the literal-password hint is missing:\n%s", plain(d.render()))
	}
	d.onKey("ctrl+s", "")
	if len(fakeCfg.saved) != 1 || fakeCfg.savedFrom[0] != "appdb" || !fakeCfg.saved[0].PasswordLiteral || fakeCfg.saved[0].PasswordEnv != "" {
		t.Errorf("saved = %+v from %v (an untouched literal password must be kept)", fakeCfg.saved, fakeCfg.savedFrom)
	}
}

func TestFormShowsValidationProblemsAndStaysOpen(t *testing.T) {
	d, fakeCfg := formManager(t)
	fakeCfg.saveErr = &configedit.InvalidError{Message: "database name is required"}
	model, _ := d.onKey("a", "")
	model, _ = model.(manager).onKey("ctrl+s", "")
	d = model.(manager)
	if d.mode != modeForm || !strings.Contains(plain(d.render()), "database name is required") {
		t.Errorf("want the problem in the form (mode %d):\n%s", d.mode, plain(d.render()))
	}

	d.form.field("port").value = "abc"
	model, _ = d.onKey("ctrl+s", "")
	d = model.(manager)
	if !strings.Contains(d.form.problem, "not a number") || len(fakeCfg.saved) != 1 {
		t.Errorf("port problem = %q, saves = %d (a bad number must not reach the save)", d.form.problem, len(fakeCfg.saved))
	}
	d.form.field("port").value = ""
	d.form.field("retention").value = "daily=x"
	model, _ = d.onKey("ctrl+s", "")
	if !strings.Contains(model.(manager).form.problem, "retention") {
		t.Errorf("retention problem = %q", model.(manager).form.problem)
	}
}

func TestUnexpectedSaveFailureLeavesTheFormWithANotice(t *testing.T) {
	d, fakeCfg := formManager(t)
	fakeCfg.saveErr = errors.New("permission denied")
	model, _ := d.onKey("a", "")
	d = typeText(t, model.(manager), "")
	d.form.field("name").value, d.form.field("database").value, d.form.field("repositories").value = "x", "x", "s3"
	model, _ = d.onKey("ctrl+s", "")
	d = model.(manager)
	if d.mode != modeTable || !d.noticeBad || !strings.Contains(d.notice, "permission denied") {
		t.Errorf("mode %d notice %q", d.mode, d.notice)
	}
}

func TestFilesJobsCannotBeEditedOrRemovedHere(t *testing.T) {
	d, fakeCfg := formManager(t)
	d.cursor = 1 // the files line of the report ("files" job failed run)
	model, _ := d.onKey("e", "")
	d = model.(manager)
	if d.mode != modeTable || !strings.Contains(d.notice, "only database jobs") {
		t.Errorf("edit files job: mode %d notice %q", d.mode, d.notice)
	}
	model, _ = d.onKey("d", "")
	d = model.(manager)
	if d.mode != modeTable || !strings.Contains(d.notice, "only those can be removed") || len(fakeCfg.deleted) != 0 {
		t.Errorf("delete files job: mode %d notice %q", d.mode, d.notice)
	}
}

func TestRemoveNeedsTheJobName(t *testing.T) {
	d, fakeCfg := formManager(t)
	model, _ := d.onKey("d", "")
	d = model.(manager)
	if d.mode != modeDelete || !strings.Contains(plain(d.render()), "Remove appdb") {
		t.Fatalf("delete screen missing (mode %d):\n%s", d.mode, plain(d.render()))
	}
	d = typeText(t, d, "nope")
	model, _ = d.onKey("enter", "")
	d = model.(manager)
	if len(fakeCfg.deleted) != 0 || d.inputError == "" {
		t.Fatal("a wrong name must not remove the job")
	}
	for range "nope" {
		model, _ = d.onKey("backspace", "")
		d = model.(manager)
	}
	d = typeText(t, d, "appdb")
	model, _ = d.onKey("enter", "")
	d = model.(manager)
	if len(fakeCfg.deleted) != 1 || fakeCfg.deleted[0] != "appdb" || d.mode != modeTable || !strings.Contains(d.notice, "snapshots stay") {
		t.Errorf("deleted %v mode %d notice %q", fakeCfg.deleted, d.mode, d.notice)
	}
}

func TestFormKeysOnOtherTabsDoNothing(t *testing.T) {
	d, _ := formManager(t)
	model, _ := d.onKey("2", "")
	model, _ = model.(manager).onKey("a", "")
	if model.(manager).mode != modeTable {
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
