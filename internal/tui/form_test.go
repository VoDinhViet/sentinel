package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/config"
	"sentinel/internal/configedit"
	"sentinel/internal/status"
)

type fakeConfig struct {
	listing   configedit.Listing
	saved     []configedit.Job
	savedFrom []string
	deleted   []string
	saveErr   error

	// The other parts of the config file, for the Config tab.
	overview     configedit.Overview
	repositories []configedit.Repository
	checks       []configedit.Uptime
	notifiers    []configedit.Notifier
	settings     []configedit.Settings
	removed      []string // "repository s3", "uptime web", "notifier telegram"
	tested       []string // the notifier kinds a test message was sent to
	testErr      error
	connected    []configedit.Job // the jobs a connection test was run for
	connectedAs  []string         // the original name of each
	connectErr   error
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
	actions.overview = func() (configedit.Overview, error) { return f.overview, nil }
	actions.saveRepository = func(_ string, repository configedit.Repository) error {
		f.repositories = append(f.repositories, repository)
		return f.saveErr
	}
	actions.saveUptime = func(_ string, check configedit.Uptime) error {
		f.checks = append(f.checks, check)
		return f.saveErr
	}
	actions.saveNotifier = func(notifier configedit.Notifier) error {
		f.notifiers = append(f.notifiers, notifier)
		return f.saveErr
	}
	actions.saveSettings = func(settings configedit.Settings) error {
		f.settings = append(f.settings, settings)
		return f.saveErr
	}
	actions.testConnection = func(_ context.Context, job configedit.Job, original string) (time.Duration, error) {
		f.connected = append(f.connected, job)
		f.connectedAs = append(f.connectedAs, original)
		return 42 * time.Millisecond, f.connectErr
	}
	actions.testNotifier = func(_ context.Context, kind string) error {
		f.tested = append(f.tested, kind)
		return f.testErr
	}
	actions.deleteRepository = func(name string) error { f.removed = append(f.removed, "repository "+name); return nil }
	actions.deleteUptime = func(name string) error { f.removed = append(f.removed, "uptime "+name); return nil }
	actions.deleteNotifier = func(kind string) error { f.removed = append(f.removed, "notifier "+kind); return nil }
}

func formModel(t *testing.T) (manageModel, *fakeConfig) {
	t.Helper()
	configFile := &fakeConfig{listing: configedit.Listing{
		Repositories: []string{"local", "s3"},
		Jobs: []configedit.Job{
			{Name: "appdb", Type: config.JobPostgres, Database: "app", Host: "db", Port: 5433, PasswordLiteral: true, Repositories: []string{"s3"}, Retention: config.Retention{Daily: 7}},
			{Name: "files", Type: config.JobFiles, Repositories: []string{"local"}},
		},
	}, overview: configedit.Overview{
		Repositories: []configedit.Repository{
			{Name: "local", URL: "/var/backups/repo", PasswordEnv: "RESTIC_PW", Env: map[string]string{}},
			{Name: "s3", URL: "s3:https://minio.example.com/bucket/web-01", PasswordLiteral: true, Env: map[string]string{awsKeyID: "MINIO_ID", awsSecretKey: "MINIO_SECRET"}, UsedBy: []string{"appdb"}},
		},
		Uptime:    []configedit.Uptime{{Name: "web", Type: "http", URL: "https://example.com", Interval: "30s"}},
		Notifiers: []configedit.Notifier{{Kind: "telegram", Values: map[string]string{"token": "TG_TOKEN", "chat_id": "-100"}, Literal: map[string]bool{}}},
		Settings:  configedit.Settings{ServerName: "web-01", DefaultSchedule: "daily 02:00", DefaultRetention: config.Retention{Daily: 7}},
	}}
	actions := (&fakeActions{}).actions()
	configFile.attach(&actions)
	screen := newManageModel(t.Context(), func() (status.Report, error) { return sampleReport(), nil }, actions, jobRepositoriesOf(configFile.listing))
	model, _ := screen.Update(reportMsg{report: sampleReport()})
	return model.(manageModel), configFile
}

// keys presses named keys in order.
func keys(t *testing.T, screen manageModel, names ...string) manageModel {
	t.Helper()
	for _, name := range names {
		model, _ := screen.onKey(name, "")
		screen = model.(manageModel)
	}
	return screen
}

func openAddForm(t *testing.T, screen manageModel) manageModel {
	t.Helper()
	return keys(t, screen, "a")
}

// runDates matches a run time of the schedule preview ("02/01" or "02/01 15:04").
var runDates = regexp.MustCompile(`\d\d/\d\d`)

func TestAddDatabaseJobThroughTheForm(t *testing.T) {
	screen, configFile := formModel(t)
	screen = openAddForm(t, screen)
	if screen.mode != modeForm || !strings.Contains(plain(screen.render()), "Thêm job") {
		t.Fatalf("form missing (mode %d):\n%s", screen.mode, plain(screen.render()))
	}
	screen = keys(t, screen, "right") // type: postgres -> mysql
	if screen.form.field("type").value != config.JobMySQL || !strings.Contains(plain(screen.render()), "Tuỳ chọn nâng cao") || strings.Contains(plain(screen.render()), "TLS") || strings.Contains(plain(screen.render()), "Auth database") {
		t.Fatalf("type-specific fields wrong:\n%s", plain(screen.render()))
	}
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "shop")
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "shopdb")
	screen = keys(t, screen, "tab", "tab", "tab") // host, port, user
	screen = typeText(t, screen, "dump")
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "MYSQL_PW")
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "daily 02:30")
	screen = keys(t, screen, "tab", "tab", "right", "right") // time, then storage: automatic -> folder -> repository
	screen = keys(t, screen, "tab")                          // repositories: local, s3
	screen = keys(t, screen, " ", "right", " ")              // tick both
	screen = keys(t, screen, "tab")                          // retention: last, daily, weekly...
	screen = keys(t, screen, "right")
	screen = typeText(t, screen, "7")
	screen = keys(t, screen, "right")
	screen = typeText(t, screen, "4")
	screen = keys(t, screen, "tab", "enter") // the advanced line, opened
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "12h") // Timeout
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "disabled") // TLS, the last visible field
	screen = keys(t, screen, "enter")
	if len(configFile.saved) != 1 {
		t.Fatalf("not saved (mode %d): %+v\n%s", screen.mode, screen.form.problems(), plain(screen.render()))
	}
	got := configFile.saved[0]
	if configFile.savedFrom[0] != "" || got.Name != "shop" || got.Type != config.JobMySQL || got.Database != "shopdb" || got.User != "dump" ||
		got.PasswordEnv != "MYSQL_PW" || got.Schedule != "daily 02:30" || strings.Join(got.Repositories, ",") != "local,s3" ||
		got.Retention != (config.Retention{Daily: 7, Weekly: 4}) || got.TLS != "disabled" || got.Timeout != "12h" {
		t.Errorf("job saved = %+v", got)
	}
	if screen.mode != modeTable || !strings.Contains(screen.notice, "Đã thêm job shop") || !strings.Contains(screen.notice, "Khởi động lại daemon") {
		t.Errorf("notice = %q (mode %d)", screen.notice, screen.mode)
	}
}

func TestEditFormIsPrefilledKeepsTheTypeFixedAndExplainsLiteralPasswords(t *testing.T) {
	screen, configFile := formModel(t)
	screen = keys(t, screen, "e")
	view := plain(screen.render())
	for _, want := range []string{"Sửa appdb", "appdb", "5433", "[x] s3", "[ ] local", "daily"} {
		if !strings.Contains(view, want) {
			t.Errorf("form lacks %q:\n%s", want, view)
		}
	}
	if screen = keys(t, screen, "right"); screen.form.field("type").value != config.JobPostgres {
		t.Error("the type of an existing job cannot be cycled")
	}
	screen = keys(t, screen, "tab", "tab", "tab", "tab", "tab", "tab") // to the password env field
	if !strings.Contains(plain(screen.render()), "mật khẩu ghi thẳng") {
		t.Errorf("the literal-password hint is missing:\n%s", plain(screen.render()))
	}
	keys(t, screen, "ctrl+s")
	if len(configFile.saved) != 1 || configFile.savedFrom[0] != "appdb" || !configFile.saved[0].PasswordLiteral || configFile.saved[0].PasswordEnv != "" {
		t.Errorf("saved = %+v from %v (an untouched literal password must be kept)", configFile.saved, configFile.savedFrom)
	}
}

func TestFormShowsProblemsUnderTheFieldOnlyOnceVisitedOrTried(t *testing.T) {
	screen, configFile := formModel(t)
	screen = openAddForm(t, screen)
	if view := plain(screen.render()); strings.Contains(view, "✗") {
		t.Errorf("a pristine form must not shout:\n%s", view)
	}
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "a b")
	if view := plain(screen.render()); !strings.Contains(view, "✗ tên job") {
		t.Errorf("the name problem shows as soon as it is typed:\n%s", view)
	}
	if strings.Contains(plain(screen.render()), "cần nhập tên database") {
		t.Error("the database field has not been visited yet")
	}

	screen = keys(t, screen, "ctrl+s") // a save attempt shows everything and jumps to the first problem
	if screen.mode != modeForm || len(configFile.saved) != 0 || screen.form.fields[screen.form.focus].key != "name" || !screen.form.attempted {
		t.Fatalf("an invalid form must stay open on the first problem (mode %d, focus %q)", screen.mode, screen.form.fields[screen.form.focus].key)
	}
	screen.form.field("name").value = "ok"
	if view := plain(screen.render()); !strings.Contains(view, "✗ cần nhập tên database") {
		t.Errorf("after a save attempt every problem shows:\n%s", view)
	}
}

func TestFormRefusesLettersInNumberFieldsAndNegativeCounts(t *testing.T) {
	screen, _ := formModel(t)
	screen = openAddForm(t, screen)
	screen = keys(t, screen, "tab", "tab", "tab", "tab") // port
	screen = typeText(t, screen, "5x4y3")
	if got := screen.form.field("port").value; got != "543" {
		t.Errorf("port = %q, digits only", got)
	}
	screen.form.fields[screen.form.focus].value = ""
	retention := screen.form.field("retention")
	for i := 0; i < 8; i++ {
		screen.form.typeInto(retention, "9", "9")
	}
	if len(retention.counts[0]) != 4 {
		t.Errorf("a count has at most 4 digits: %q", retention.counts[0])
	}
}

func TestSaveRefusedByTheConfigStaysInTheFormUnderItsField(t *testing.T) {
	screen, configFile := formModel(t)
	configFile.saveErr = &configedit.InvalidError{Message: "the change makes the config invalid"}
	screen = openAddForm(t, screen)
	screen.form.field("name").value, screen.form.field("database").value = "x", "x"
	screen.form.field("storage").value, screen.form.field("repositories").ticked[1] = storageRepository, true
	screen = keys(t, screen, "ctrl+s")
	if screen.mode != modeForm || !strings.Contains(plain(screen.render()), "the change makes the config invalid") || len(configFile.saved) != 1 {
		t.Errorf("want the config's refusal in the form (mode %d):\n%s", screen.mode, plain(screen.render()))
	}
}

func TestUnexpectedSaveFailureLeavesTheFormWithANotice(t *testing.T) {
	screen, configFile := formModel(t)
	configFile.saveErr = errors.New("permission denied")
	screen = openAddForm(t, screen)
	screen.form.field("name").value, screen.form.field("database").value = "x", "x"
	screen.form.field("storage").value, screen.form.field("repositories").ticked[1] = storageRepository, true
	screen = keys(t, screen, "ctrl+s")
	if screen.mode != modeTable || !screen.noticeBad || !strings.Contains(screen.notice, "permission denied") {
		t.Errorf("mode %d notice %q", screen.mode, screen.notice)
	}
}

func TestScheduleFieldCyclesPresetsAndPreviewsInPlainWords(t *testing.T) {
	screen, configFile := formModel(t)
	configFile.listing.Defaults = config.Defaults{Schedule: "daily 02:00", Retention: config.Retention{Daily: 7}}
	configFile.listing.InheritedRepositories = []string{"local"}
	screen = openAddForm(t, screen)
	screen.form.field("name").value, screen.form.field("database").value = "inherits", "x"
	screen = keys(t, screen, "tab", "tab", "tab", "tab", "tab", "tab", "tab") // schedule
	if view := plain(screen.render()); !strings.Contains(view, "mặc định: daily 02:00") || !strings.Contains(view, "→ mỗi ngày lúc 02:00") {
		t.Errorf("an empty schedule shows the default and what it means:\n%s", view)
	}
	screen = keys(t, screen, "right")
	if got := screen.form.field("schedule").value; got != "every 15m" {
		t.Errorf("→ starts after manual: %q", got)
	}
	screen = keys(t, screen, "right", "right", "right", "right")
	if got := screen.form.field("schedule").value; got != "weekly sun 02:00" {
		t.Errorf("how often cycles every 15m, hourly, every 6h, daily, weekly: %q", got)
	}
	screen = keys(t, screen, "alt+up", "alt+up", "alt+right") // 03:00, Monday
	if got := screen.form.field("schedule").value; got != "weekly mon 03:00" {
		t.Errorf("alt+↑ moves the time, alt+→ the weekday: %q", got)
	}
	view := plain(screen.render())
	if !strings.Contains(view, "mỗi Thứ Hai lúc 03:00") || !strings.Contains(view, "sau đó ") || len(runDates.FindAllString(view, -1)) < 3 {
		t.Errorf("preview names the schedule and lists three runs:\n%s", view)
	}
	screen = typeText(t, screen, " nonsense")
	if view := plain(screen.render()); !strings.Contains(view, "✗ schedule không hợp lệ") {
		t.Errorf("a bad schedule is flagged as it is typed:\n%s", view)
	}

	// With defaults in the config a job needs no repository of its own.
	screen.form.field("schedule").value = ""
	screen = keys(t, screen, "ctrl+s")
	if len(configFile.saved) != 1 || len(configFile.saved[0].Repositories) != 0 || configFile.saved[0].Schedule != "" {
		t.Errorf("saved %+v: nothing ticked and no schedule means the defaults", configFile.saved)
	}
}

// fieldIndex is the position of a form field, for the click actions that name it.
func fieldIndex(screen manageModel, key string) int {
	for i, field := range screen.form.fields {
		if field.key == key {
			return i
		}
	}
	return -1
}

func TestMouseOperatesTheForm(t *testing.T) {
	screen, configFile := formModel(t)
	screen = openAddForm(t, screen)

	screen, _ = clickOn(t, screen, "step:0:1") // the box of the type
	if screen.form.field("type").value != config.JobMySQL {
		t.Errorf("clicking the box chooses the next type: %q", screen.form.field("type").value)
	}
	screen, _ = clickOn(t, screen, fmt.Sprintf("step:%d:1", fieldIndex(screen, "storage"))) // automatic -> folder
	screen, _ = clickOn(t, screen, fmt.Sprintf("step:%d:1", fieldIndex(screen, "storage"))) // -> repository
	if screen.form.field("storage").value != storageRepository {
		t.Errorf("clicking Storage steps through its choices: %q", screen.form.field("storage").value)
	}
	screen, _ = clickOn(t, screen, fmt.Sprintf("toggle:%d:1", fieldIndex(screen, "repositories"))) // tick s3
	if !screen.form.field("repositories").ticked[1] {
		t.Error("clicking a repository ticks it")
	}
	screen, _ = clickOn(t, screen, fmt.Sprintf("toggle:%d:1", fieldIndex(screen, "repositories")))
	if screen.form.field("repositories").ticked[1] {
		t.Error("clicking again unticks it")
	}
	screen, _ = clickOn(t, screen, fmt.Sprintf("focus:%d", fieldIndex(screen, "retention")))   // the summary opens the counts
	screen, _ = clickOn(t, screen, fmt.Sprintf("focus:%d:3", fieldIndex(screen, "retention"))) // the monthly count
	if screen.form.fields[screen.form.focus].key != "retention" || screen.form.field("retention").sub != 3 {
		t.Errorf("clicking a count focuses it: %+v", screen.form.field("retention"))
	}
	screen = typeText(t, screen, "6")
	if screen.form.field("retention").counts[3] != "6" {
		t.Errorf("typing goes to the clicked count: %+v", screen.form.field("retention").counts)
	}
	screen, _ = clickOn(t, screen, "focus:1")
	if screen.form.fields[screen.form.focus].key != "name" {
		t.Error("clicking a field focuses it")
	}

	screen.form.field("name").value, screen.form.field("database").value = "shop", "shop"
	screen.form.field("repositories").ticked[0] = true
	screen, _ = clickOn(t, screen, "ctrl+s") // the [ Save ] button
	if len(configFile.saved) != 1 || screen.mode != modeTable {
		t.Errorf("the Save button saves (mode %d, saved %d)", screen.mode, len(configFile.saved))
	}
	screen = openAddForm(t, screen)
	screen, _ = clickOn(t, screen, "esc") // [ Cancel ]
	if screen.mode != modeTable {
		t.Error("the Cancel button closes the form")
	}
}

func TestFilesJobsAreEditedThroughTheSameForm(t *testing.T) {
	screen, configFile := formModel(t)
	screen.cursor = 1 // the "files" job
	screen = keys(t, screen, "e")
	if screen.mode != modeForm || screen.form.original != "files" {
		t.Fatalf("edit files job: mode %d notice %q", screen.mode, screen.notice)
	}
	view := plain(screen.render())
	if !strings.Contains(view, "Đường dẫn") || !strings.Contains(view, "Tuỳ chọn nâng cao") || strings.Contains(view, "Database") || strings.Contains(view, "Env var mật khẩu") {
		t.Errorf("a files job shows its own fields only:\n%s", view)
	}
	screen = keys(t, screen, "tab", "tab") // paths
	screen = typeText(t, screen, "/tmp, /nonexistent-sentinel-dir")
	if view := plain(screen.render()); !strings.Contains(view, "▲ không thấy ở đây: /nonexistent-sentinel-dir") {
		t.Errorf("a missing path is called out:\n%s", view)
	}
	screen = keys(t, screen, "ctrl+s")
	if len(configFile.saved) != 1 || strings.Join(configFile.saved[0].Paths, "|") != "/tmp|/nonexistent-sentinel-dir" || configFile.saved[0].Type != config.JobFiles {
		t.Errorf("saved = %+v", configFile.saved)
	}
}

func TestNewFilesJobNeedsAPath(t *testing.T) {
	screen, configFile := formModel(t)
	screen = openAddForm(t, screen)
	screen.form.field("type").value = config.JobFiles
	screen.form.field("name").value = "docs"
	screen.form.field("repositories").ticked[0] = true
	screen = keys(t, screen, "ctrl+s")
	if screen.mode != modeForm || len(configFile.saved) != 0 || screen.form.fields[screen.form.focus].key != "paths" {
		t.Fatalf("a files job without a path must stay in the form on the paths (mode %d, focus %q)", screen.mode, screen.form.fields[screen.form.focus].key)
	}
	if !strings.Contains(plain(screen.render()), "✗ nhập ít nhất một đường dẫn") {
		t.Errorf("the problem is shown under Paths:\n%s", plain(screen.render()))
	}
}

func TestRemoveNeedsTheJobName(t *testing.T) {
	screen, configFile := formModel(t)
	model, _ := screen.onKey("d", "")
	screen = model.(manageModel)
	if _, ok := screen.top().(deleteScreen); !ok || !strings.Contains(plain(screen.render()), "Xoá appdb") {
		t.Fatalf("delete screen missing (screens %d):\n%s", len(screen.screens), plain(screen.render()))
	}
	screen = typeText(t, screen, "nope")
	model, _ = screen.onKey("enter", "")
	screen = model.(manageModel)
	if open, _ := screen.top().(deleteScreen); len(configFile.deleted) != 0 || open.problem == "" {
		t.Fatal("a wrong name must not remove the job")
	}
	for range "nope" {
		model, _ = screen.onKey("backspace", "")
		screen = model.(manageModel)
	}
	screen = typeText(t, screen, "appdb")
	model, _ = screen.onKey("enter", "")
	screen = model.(manageModel)
	if len(configFile.deleted) != 1 || configFile.deleted[0] != "appdb" || !screen.onTabs() || !strings.Contains(screen.notice, "snapshot đã có vẫn nằm") {
		t.Errorf("deleted %v screens %d notice %q", configFile.deleted, len(screen.screens), screen.notice)
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

func TestFolderNoteTellsWhetherTheBackupFolderIsUsable(t *testing.T) {
	existing := t.TempDir()
	t.Setenv("SENTINEL_TEST_DIR", existing)
	for name, tc := range map[string]struct{ folder, want string }{
		"existing folder":        {existing, "✓ đã có, ghi được"},
		"free space is shown":    {existing, "trên ổ đĩa này"},
		"folder to be created":   {existing + "/backups/web-01", "✓ sẽ được tạo"},
		"variables are expanded": {"${SENTINEL_TEST_DIR}/new", "✓ sẽ được tạo"},
	} {
		if got := plain(folderNote(tc.folder)); !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q lacks %q", name, got, tc.want)
		}
	}
	if got := folderNote("  "); got != "" {
		t.Errorf("an empty folder has no note: %q", got)
	}
}

func TestRepositoryFormShowsTheFolderNoteForLocalRepositoriesOnly(t *testing.T) {
	screen, _ := configScreen(t)
	screen = keys(t, screen, "a")
	screen.form.field("path").value = t.TempDir()
	if view := plain(screen.render()); !strings.Contains(view, "✓ đã có, ghi được") {
		t.Errorf("a local folder gets a note:\n%s", view)
	}
	screen.form.field("kind").value = "rclone"
	screen.form.field("path").value = t.TempDir()
	if view := plain(screen.render()); strings.Contains(view, "đã có, ghi được") {
		t.Errorf("a remote path is not checked on this disk:\n%s", view)
	}
}

func TestTabCompletesTheLastPathThenMovesOn(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var", "www"), 0o755); err != nil {
		t.Fatal(err)
	}
	screen := loadedModel(t)
	screen.form = newJobForm(configedit.Job{Type: config.JobFiles, Name: "web"}, "", configedit.Listing{})
	screen.mode = modeForm
	screen.form.focus = fieldIndex(screen, "paths")
	screen.form.fields[screen.form.focus].value = "/etc, " + root + "/v"

	screen, _ = press(t, screen, "tab")
	if got, want := screen.form.fields[screen.form.focus].value, "/etc, "+root+"/var/"; got != want || screen.form.fields[screen.form.focus].key != "paths" {
		t.Fatalf("after tab: %q (field %q), want %q on paths", got, screen.form.fields[screen.form.focus].key, want)
	}
	screen, _ = press(t, screen, "tab") // www/ is the only entry: it is completed too
	if got := screen.form.fields[screen.form.focus].value; got != "/etc, "+root+"/var/www/" {
		t.Fatalf("second tab: %q", got)
	}
	screen, _ = press(t, screen, "tab") // nothing left to complete: tab moves on
	if screen.form.fields[screen.form.focus].key == "paths" {
		t.Errorf("tab must move to the next field when there is nothing to complete")
	}
}

func TestTabListsCandidatesOnceThenMovesOn(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"aa1", "aa2"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	screen := loadedModel(t)
	screen.form = newJobForm(configedit.Job{Type: config.JobFiles, Name: "web"}, "", configedit.Listing{})
	screen.mode = modeForm
	screen.form.focus = fieldIndex(screen, "paths")
	screen.form.fields[screen.form.focus].value = root + "/a"
	screen, _ = press(t, screen, "tab")
	field := screen.form.fields[screen.form.focus]
	if field.value != root+"/aa" || len(field.options) != 2 || !strings.Contains(plain(screen.render()), "aa1/  aa2/") {
		t.Fatalf("value %q options %v\n%s", field.value, field.options, plain(screen.render()))
	}
	screen, _ = press(t, screen, "tab") // same candidates again: move on
	if screen.form.fields[screen.form.focus].key == "paths" {
		t.Error("a second tab with the same candidates must move on")
	}
}

func TestTabCompletesTheRestoreFolder(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "restore-here"), 0o755); err != nil {
		t.Fatal(err)
	}
	screen := loadedModel(t)
	screen = screen.push(targetScreen{target: root + "/res"})
	screen, _ = press(t, screen, "tab")
	if asked, _ := openScreen[targetScreen](screen); asked.target != root+"/restore-here/" {
		t.Errorf("target = %q", asked.target)
	}
}

func TestStoreAtCreatesTheRepositoryAndTicksItForTheJob(t *testing.T) {
	screen, configFile := formModel(t)
	screen = openAddForm(t, screen)
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "orders")
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "ordersdb")
	screen.form.field("storage").value = storageFolder
	screen.form.field("store_at").value = t.TempDir() + "/dumps"
	if problems := screen.form.problems(); len(problems) != 0 {
		t.Fatalf("a Store at folder stands in for a ticked repository: %v", problems)
	}
	model, _ := screen.saveForm()
	screen = model.(manageModel)
	if len(configFile.saved) != 1 || strings.Join(configFile.saved[0].Repositories, ",") != "orders" {
		t.Fatalf("job saved = %+v (notice %q)", configFile.saved, screen.notice)
	}
	if len(configFile.repositories) != 1 || configFile.repositories[0].Name != "orders" || !strings.HasSuffix(configFile.repositories[0].URL, "/dumps") || configFile.repositories[0].PasswordEnv != "RESTIC_PW" {
		t.Errorf("repository saved = %+v", configFile.repositories)
	}
	if !strings.Contains(screen.notice, "được tạo ở lần backup đầu tiên") {
		t.Errorf("notice = %q, want to hear the repository is created by the first backup", screen.notice)
	}
}

func TestStoreAtRefusesAnExistingRepositoryName(t *testing.T) {
	screen, _ := formModel(t)
	screen = openAddForm(t, screen)
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "local") // a repository of that name exists
	screen.form.field("storage").value = storageFolder
	screen.form.field("store_at").value = "/srv/backups"
	if message := screen.form.problems()["store_at"]; !strings.Contains(message, `"local" đã tồn tại`) {
		t.Errorf("problems = %v", screen.form.problems())
	}
}

func TestAltArrowsAreNamedTheWayTheFormExpects(t *testing.T) {
	for want, press := range map[string]tea.KeyPressMsg{
		"alt+up":    {Code: tea.KeyUp, Mod: tea.ModAlt},
		"alt+down":  {Code: tea.KeyDown, Mod: tea.ModAlt},
		"alt+left":  {Code: tea.KeyLeft, Mod: tea.ModAlt},
		"alt+right": {Code: tea.KeyRight, Mod: tea.ModAlt},
	} {
		if got := press.String(); got != want {
			t.Errorf("key string = %q, want %q", got, want)
		}
	}
}

func TestDatabaseJobWithoutARepositoryGetsAnAutomaticFolder(t *testing.T) {
	screen, configFile := formModel(t)
	screen = openAddForm(t, screen)
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "ordersdb")
	screen = keys(t, screen, "tab")
	screen = typeText(t, screen, "orders")
	if view := plain(screen.render()); !strings.Contains(view, "tự động: "+autoStoreBase()+"/ordersdb") {
		t.Errorf("the form says where the backups will go:\n%s", view)
	}
	if problems := screen.form.problems(); len(problems) != 0 {
		t.Fatalf("no folder and no repository is fine for a database job: %v", problems)
	}
	model, _ := screen.saveForm()
	screen = model.(manageModel)
	if len(configFile.saved) != 1 || strings.Join(configFile.saved[0].Repositories, ",") != "ordersdb" {
		t.Fatalf("job saved = %+v (notice %q)", configFile.saved, screen.notice)
	}
	if len(configFile.repositories) != 1 || configFile.repositories[0].URL != autoStoreBase()+"/ordersdb" {
		t.Errorf("repository saved = %+v", configFile.repositories)
	}
	if !strings.Contains(screen.notice, autoStoreBase()+"/ordersdb") || !strings.Contains(screen.notice, "được tạo ở lần backup đầu tiên") {
		t.Errorf("notice = %q", screen.notice)
	}
}

func TestAutomaticFolderIsOnlyForAutomaticStorageWithNoDefaultRepository(t *testing.T) {
	screen, _ := formModel(t)
	screen = openAddForm(t, screen)
	screen.form.field("name").value, screen.form.field("database").value = "files1", "x"
	screen.form.field("type").value = config.JobFiles
	screen.form.field("paths").value = "/etc"
	if _, _, repo, _ := screen.form.jobWithStore(); repo.URL != autoStoreBase()+"/files1" {
		t.Errorf("a files job with automatic storage gets a folder too: %+v", repo)
	}
	screen.form.field("storage").value = storageRepository
	if message := screen.form.problems()["repositories"]; message == "" {
		t.Error("choosing Repository without ticking one is refused")
	}
	screen.form.field("repositories").ticked[0] = true
	if _, _, repo, _ := screen.form.jobWithStore(); repo.URL != "" {
		t.Errorf("a ticked repository means no automatic one: %+v", repo)
	}
	screen.form.field("storage").value = storageAuto
	screen.form.known.InheritsRepositories = true
	if _, _, repo, _ := screen.form.jobWithStore(); repo.URL != "" {
		t.Errorf("repositories from the defaults mean no automatic one: %+v", repo)
	}
}

func TestTimeAndDayRowsFollowTheSchedule(t *testing.T) {
	screen, _ := formModel(t)
	screen = openAddForm(t, screen)
	schedule := screen.form.field("schedule")
	for _, key := range []string{"schedule_time", "schedule_day"} {
		if slices.Contains(screen.form.visible(), fieldIndex(screen, key)) {
			t.Errorf("%s shows with no schedule", key)
		}
	}
	schedule.value = "weekly mon 03:00"
	screen.form.syncSchedules()
	if got := screen.form.field("schedule_time").value; got != "03:00" {
		t.Errorf("time row = %q", got)
	}
	if got := screen.form.field("schedule_day").value; got != "Thứ Hai" {
		t.Errorf("day row = %q", got)
	}
	screen.form.focus = fieldIndex(screen, "schedule_time")
	screen = typeText(t, screen, "9:45")
	if got := screen.form.field("schedule").value; got != "weekly mon 09:45" {
		t.Errorf("typing a time rewrites the schedule: %q", got)
	}
	screen = keys(t, screen, "right")
	if got := screen.form.field("schedule").value; got != "weekly mon 10:15" {
		t.Errorf("→ on the time moves 30 minutes: %q", got)
	}
	screen.form.focus = fieldIndex(screen, "schedule_day")
	screen = keys(t, screen, "right")
	if got := screen.form.field("schedule").value; got != "weekly tue 10:15" {
		t.Errorf("→ on the day moves the weekday: %q", got)
	}
	if view := plain(screen.render()); !strings.Contains(view, "[ Weekly ▾ ]") || !strings.Contains(view, "[ 10:15") || !strings.Contains(view, "[ Thứ Ba ▾ ]") {
		t.Errorf("the schedule is drawn as boxes:\n%s", view)
	}
}

// connectForm is the add form for a postgres job with the connection fields filled in.
func connectForm(t *testing.T) (manageModel, *fakeConfig) {
	t.Helper()
	screen, configFile := formModel(t)
	screen = openAddForm(t, screen)
	screen.form.field("name").value, screen.form.field("database").value = "shop", "shopdb"
	screen.form.field("host").value, screen.form.field("user").value, screen.form.field("password_env").value = "db.local", "dump", "SHOP_PW"
	return screen, configFile
}

func TestConnectButtonTestsTheDatabaseOfTheForm(t *testing.T) {
	screen, configFile := connectForm(t)
	if view := plain(screen.render()); !strings.Contains(view, "[ Kết nối thử ]") || !strings.Contains(view, "ctrl+t") {
		t.Fatalf("a database job form offers the connection test:\n%s", view)
	}
	model, cmd := screen.onKey("ctrl+t", "")
	screen = model.(manageModel)
	if view := plain(screen.render()); !strings.Contains(view, "đang thử") {
		t.Errorf("the form says it is trying:\n%s", view)
	}
	screen = feed(t, screen, messagesOf(cmd))
	if len(configFile.connected) != 1 || configFile.connected[0].Database != "shopdb" || configFile.connected[0].Host != "db.local" ||
		configFile.connected[0].User != "dump" || configFile.connected[0].PasswordEnv != "SHOP_PW" || configFile.connectedAs[0] != "" {
		t.Fatalf("tested %+v as %q", configFile.connected, configFile.connectedAs)
	}
	if view := plain(screen.render()); !strings.Contains(view, "✓ kết nối được (42ms)") {
		t.Errorf("the result is shown beside the button:\n%s", view)
	}

	// Clicking the button does the same.
	screen, cmd = clickOn(t, screen, "ctrl+t")
	feed(t, screen, messagesOf(cmd))
	if len(configFile.connected) != 2 {
		t.Errorf("clicking the button runs a test: %d", len(configFile.connected))
	}
}

func TestConnectFailureIsShownAndDoesNotBlockSaving(t *testing.T) {
	screen, configFile := connectForm(t)
	configFile.connectErr = errors.New(`FATAL: password authentication failed for user "dump"`)
	model, cmd := screen.onKey("ctrl+t", "")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	if view := plain(screen.render()); !strings.Contains(view, "✗ FATAL: password authentication failed") {
		t.Errorf("the reason is shown:\n%s", view)
	}
	screen.form.field("storage").value = storageFolder
	screen.form.field("store_at").value = t.TempDir() + "/dumps"
	screen = keys(t, screen, "ctrl+s")
	if len(configFile.saved) != 1 {
		t.Errorf("a failed test does not stop the save (mode %d): %v", screen.mode, screen.form.problems())
	}
}

func TestConnectResultIsDroppedWhenTheConnectionFieldsChange(t *testing.T) {
	screen, _ := connectForm(t)
	model, cmd := screen.onKey("ctrl+t", "")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	if screen.form.connect.text == "" {
		t.Fatal("no result to drop")
	}
	screen.form.focus = fieldIndex(screen, "host")
	screen = typeText(t, screen, "x")
	if screen.form.connect.text != "" {
		t.Errorf("a result about another host is gone: %+v", screen.form.connect)
	}

	// An answer that arrives after the fields changed is not shown.
	model, cmd = screen.onKey("ctrl+t", "")
	screen = model.(manageModel)
	late := messagesOf(cmd)
	screen = typeText(t, screen, "y")
	screen = feed(t, screen, late)
	if screen.form.connect.text != "" || screen.form.connect.running {
		t.Errorf("a late answer for old values is ignored: %+v", screen.form.connect)
	}
}

func TestConnectNeedsADatabaseJob(t *testing.T) {
	screen, configFile := formModel(t)
	screen = openAddForm(t, screen)
	screen.form.field("type").value = config.JobFiles
	model, cmd := screen.onKey("ctrl+t", "")
	screen = model.(manageModel)
	if cmd != nil || len(configFile.connected) != 0 {
		t.Error("a files job has no database to connect to")
	}
	if view := plain(screen.render()); strings.Contains(view, "Kết nối thử") {
		t.Errorf("no button for a files job:\n%s", view)
	}
}

func TestConnectUsesTheSavedJobsNameWhenEditing(t *testing.T) {
	screen, configFile := formModel(t)
	screen = keys(t, screen, "e") // the appdb job, which keeps a literal password
	model, cmd := screen.onKey("ctrl+t", "")
	feed(t, model.(manageModel), messagesOf(cmd))
	if len(configFile.connected) != 1 || configFile.connectedAs[0] != "appdb" || !configFile.connected[0].PasswordLiteral {
		t.Errorf("tested %+v as %q", configFile.connected, configFile.connectedAs)
	}
}
