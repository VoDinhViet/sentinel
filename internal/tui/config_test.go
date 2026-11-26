package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/configedit"
	"sentinel/internal/status"
)

// The Config tab shows the address an entry probes the way `sentinel check` does, so the secret in a
// health-check URL is not on the screen.
func TestUptimeEndpoint(t *testing.T) {
	tests := []struct {
		name  string
		check configedit.Uptime
		want  string
	}{
		{"http", configedit.Uptime{Type: "http", URL: "https://example.com/health"}, "https://example.com/health"},
		{"http with a token", configedit.Uptime{Type: "http", URL: "https://u:p@example.com/ping?token=abc"}, "https://example.com/ping?token=***"},
		{"tcp", configedit.Uptime{Type: "tcp", Address: "db:5432"}, "db:5432"},
		{"ping", configedit.Uptime{Type: "ping", Host: "10.0.0.1"}, "10.0.0.1"},
		{"a ${VAR} in the URL is shown as written", configedit.Uptime{Type: "http", URL: "https://${HOST}/ping?key=${KEY}"}, "https://${HOST}/ping?key=${KEY}"},
		{"no type shows the first address field", configedit.Uptime{URL: "https://example.com/health"}, "https://example.com/health"},
		{"an unknown type shows the first address field", configedit.Uptime{Type: "udp", Address: "10.0.0.5:53"}, "10.0.0.5:53"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uptimeEndpoint(tt.check); got != tt.want {
				t.Errorf("uptimeEndpoint = %q, want %q", got, tt.want)
			}
		})
	}
}

// configScreen is the formModel screen opened on the Config tab.
func configScreen(t *testing.T) (manageModel, *fakeConfig) {
	t.Helper()
	screen, configFile := formModel(t)
	model, _ := screen.Update(tea5())
	return model.(manageModel), configFile
}

// goTo selects the line of the Config tab for that section and name.
func goTo(t *testing.T, screen manageModel, section configSection, name string) manageModel {
	t.Helper()
	for i, line := range screen.tabs()[configTab].lines {
		if line.entry == (configEntry{section, name}) {
			screen.cursor = i
			return screen
		}
	}
	t.Fatalf("no %s %q on the Config tab:\n%s", sectionNames[section], name, plain(screen.render()))
	return screen
}

func TestConfigTabListsEveryPartOfTheConfig(t *testing.T) {
	screen, _ := configScreen(t)
	if screen.tab != configTab || !screen.config.loaded {
		t.Fatalf("tab %d, loaded %v", screen.tab, screen.config.loaded)
	}
	// Wide enough for every column.
	model, _ := screen.Update(wideScreen())
	view := plain(model.(manageModel).render())
	for _, want := range []string{
		"5 Config", "REPOSITORY", "ĐỊA CHỈ", "MẬT KHẨU", "local", "/var/backups/repo", "${RESTIC_PW}", "chưa job nào dùng",
		"ghi thẳng trong file", "dùng bởi appdb",
		"JOB", "LỊCH", "appdb", "postgres", "daily 7",
		"UPTIME CHECK", "web", "https://example.com", "mỗi 30s",
		"NOTIFIER", "telegram", "token ${TG_TOKEN}", "chat_id -100",
		"CÀI ĐẶT CHUNG", "TÊN MÁY", "web-01", "daily 02:00",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("Config tab lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "hunter2") {
		t.Error("a secret must never be shown")
	}
}

func TestSplitAndBuildRepositoryLocations(t *testing.T) {
	for _, tc := range []struct {
		url, kind string
		parts     map[string]string
	}{
		{"/var/backups/repo", "local", map[string]string{"path": "/var/backups/repo"}},
		{"${HOME}/.local/share/sentinel/repo", "local", map[string]string{"path": "${HOME}/.local/share/sentinel/repo"}},
		{"s3:s3.amazonaws.com/my-bucket/web-01", "s3", map[string]string{"endpoint": "", "bucket": "my-bucket", "prefix": "web-01"}},
		{"s3:https://minio.example.com/bucket/a/b", "s3", map[string]string{"endpoint": "https://minio.example.com", "bucket": "bucket", "prefix": "a/b"}},
		{"s3:minio.local:9000/bucket", "s3", map[string]string{"endpoint": "minio.local:9000", "bucket": "bucket", "prefix": ""}},
		{"rclone:gdrive:backups/web-01", "rclone", map[string]string{"remote": "gdrive", "path": "backups/web-01"}},
		{"sftp:me@host:/srv/restic", "sftp", map[string]string{"target": "me@host", "path": "/srv/restic"}},
		{"b2:bucket:path", "custom", map[string]string{"url": "b2:bucket:path"}},
		{"sftp://me@host//srv/restic", "custom", map[string]string{"url": "sftp://me@host//srv/restic"}},
	} {
		kind, parts := splitRepositoryURL(tc.url)
		if kind != tc.kind {
			t.Errorf("%s: kind %q, want %q", tc.url, kind, tc.kind)
			continue
		}
		for key, want := range tc.parts {
			if parts[key] != want {
				t.Errorf("%s: %s = %q, want %q", tc.url, key, parts[key], want)
			}
		}
		if rebuilt := buildRepositoryURL(kind, parts); rebuilt != tc.url {
			t.Errorf("%s: rebuilt as %q", tc.url, rebuilt)
		}
	}
}

func TestAddAnS3RepositoryFromTheConfigTab(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = goTo(t, screen, secRepository, "local")
	screen = keys(t, screen, "a")
	if screen.mode != modeForm || screen.form.kind != formRepository || screen.form.title != "Thêm repository" {
		t.Fatalf("mode %d form %+v", screen.mode, screen.form.title)
	}
	screen = keys(t, screen, "tab", "right") // kind: local -> s3
	if view := plain(screen.render()); !strings.Contains(view, "Bucket") || strings.Contains(view, "Thư mục  ") && strings.Contains(view, "rclone remote") {
		t.Errorf("the s3 fields:\n%s", view)
	}
	set := func(key, value string) { screen.form.field(key).value = value }
	if screen.form.field("password_env").value != "RESTIC_PASSWORD" || screen.form.field("access_key_env").value != "AWS_ACCESS_KEY_ID" {
		t.Error("a new repository starts with the usual variable names filled in")
	}
	set("name", "offsite")
	set("bucket", "my-bucket")
	set("prefix", "web-01")
	set("access_key_env", "S3_ID")
	set("secret_key_env", "S3_SECRET")
	set("password_env", "RESTIC_PW")
	screen = keys(t, screen, "ctrl+s")
	if len(configFile.repositories) != 1 {
		t.Fatalf("not saved (mode %d): %v\n%s", screen.mode, screen.form.problems(), plain(screen.render()))
	}
	got := configFile.repositories[0]
	if got.Name != "offsite" || got.URL != "s3:s3.amazonaws.com/my-bucket/web-01" || got.PasswordEnv != "RESTIC_PW" || got.Env[awsKeyID] != "S3_ID" || got.Env[awsSecretKey] != "S3_SECRET" {
		t.Errorf("saved repository = %+v", got)
	}
	if screen.mode != modeTable || !strings.Contains(screen.notice, "Đã thêm repository offsite") {
		t.Errorf("mode %d notice %q", screen.mode, screen.notice)
	}
}

func TestRepositoryFormShowsEveryMissingPartAtOnce(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = keys(t, screen, "a")
	screen.form.field("password_env").value = ""
	screen = keys(t, screen, "ctrl+s")
	view := plain(screen.render())
	for _, want := range []string{"✗ tên repository", "✗ cần nhập thư mục", "✗ nhập tên biến môi trường"} {
		if !strings.Contains(view, want) {
			t.Errorf("the form lacks %q after a save attempt:\n%s", want, view)
		}
	}
	if len(configFile.repositories) != 0 || screen.mode != modeForm {
		t.Error("an incomplete repository must not be saved")
	}
}

func TestEditRepositoryKeepsALiteralPasswordAndTheKindFields(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = goTo(t, screen, secRepository, "s3")
	screen = keys(t, screen, "e")
	form := screen.form
	if form.field("kind").value != "s3" || form.field("endpoint").value != "https://minio.example.com" || form.field("bucket").value != "bucket" ||
		form.field("prefix").value != "web-01" || form.field("access_key_env").value != "MINIO_ID" || !form.passwordKept {
		t.Fatalf("form not prefilled: %+v", form.fields)
	}
	typeText(t, screen, "ignored") // focus is on the fixed name: typing must change nothing
	screen.form.typeInto(screen.form.field("name"), "x", "x")
	if screen.form.field("name").value != "s3" {
		t.Error("the name of an existing repository cannot be changed")
	}
	keys(t, screen, "ctrl+s")
	if len(configFile.repositories) != 1 || !configFile.repositories[0].PasswordLiteral || configFile.repositories[0].PasswordEnv != "" ||
		configFile.repositories[0].URL != "s3:https://minio.example.com/bucket/web-01" {
		t.Errorf("saved = %+v (the literal password stays, the location is rebuilt as it was)", configFile.repositories)
	}
}

func TestRemoveRepositoryRefusesOneInUse(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = goTo(t, screen, secRepository, "s3")
	screen = keys(t, screen, "d")
	if screen.mode != modeTable || !strings.Contains(screen.notice, "dùng bởi appdb") || len(configFile.removed) != 0 {
		t.Errorf("mode %d notice %q", screen.mode, screen.notice)
	}
	screen = goTo(t, screen, secRepository, "local")
	screen = keys(t, screen, "d")
	if screen.mode != modeDelete {
		t.Fatalf("mode %d: %s", screen.mode, screen.notice)
	}
	screen = typeText(t, screen, "local")
	screen = keys(t, screen, "enter")
	if len(configFile.removed) != 1 || configFile.removed[0] != "repository local" || screen.mode != modeTable || !strings.Contains(screen.notice, "Đã xoá repository local") {
		t.Errorf("removed %v mode %d notice %q", configFile.removed, screen.mode, screen.notice)
	}
}

func TestUptimeFormHidesWhatTheTypeDoesNotUse(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = goTo(t, screen, secUptime, "web")
	screen = keys(t, screen, "a")
	view := plain(screen.render())
	if !strings.Contains(view, "Địa chỉ") || strings.Contains(view, "Host:port") || !strings.Contains(view, "Status mong đợi") {
		t.Errorf("http fields:\n%s", view)
	}
	screen = keys(t, screen, "tab", "right") // type: http -> tcp
	view = plain(screen.render())
	if !strings.Contains(view, "Host:port") || strings.Contains(view, "Status mong đợi") || strings.Contains(view, "Phải chứa") {
		t.Errorf("tcp fields:\n%s", view)
	}
	screen.form.field("name").value = "db"
	screen.form.field("address").value = "10.0.0.5:5432"
	screen = keys(t, screen, "ctrl+s")
	if len(configFile.checks) != 1 || configFile.checks[0].Type != "tcp" || configFile.checks[0].Address != "10.0.0.5:5432" {
		t.Errorf("saved = %+v\n%s", configFile.checks, plain(screen.render()))
	}
}

func TestNotifierFormAsksForVariableNamesAndKeepsLiteralSecrets(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = goTo(t, screen, secNotifier, "telegram")
	screen = keys(t, screen, "a") // the first kind that is not configured yet
	if screen.form.field("kind").value != "smtp" {
		t.Fatalf("new notifier kind = %q, want the first unconfigured one", screen.form.field("kind").value)
	}
	view := plain(screen.render())
	if !strings.Contains(view, "Password var") || strings.Contains(view, "Bot token") {
		t.Errorf("smtp fields:\n%s", view)
	}
	screen.form.field("smtp.host").value = "smtp.example.com"
	screen.form.field("smtp.pass").value = "SMTP_PASS"
	screen = keys(t, screen, "ctrl+s")
	if screen.mode != modeForm || !strings.Contains(plain(screen.render()), "là bắt buộc") {
		t.Errorf("a notifier without recipients stays open (mode %d):\n%s", screen.mode, plain(screen.render()))
	}
	screen.form.field("smtp.to").value = "ops@example.com, dev@example.com"
	screen = keys(t, screen, "ctrl+s")
	if len(configFile.notifiers) != 1 || configFile.notifiers[0].Kind != "smtp" || configFile.notifiers[0].Values["pass"] != "SMTP_PASS" || configFile.notifiers[0].Values["to"] != "ops@example.com, dev@example.com" {
		t.Errorf("saved = %+v", configFile.notifiers)
	}

	screen, configFile = configScreen(t)
	configFile.overview.Notifiers[0].Literal = map[string]bool{"token": true}
	configFile.overview.Notifiers[0].Values["token"] = ""
	screen = screen.refreshConfig()
	screen = goTo(t, screen, secNotifier, "telegram")
	screen = keys(t, screen, "e")
	keys(t, screen, "ctrl+s")
	if got := configFile.notifiers[len(configFile.notifiers)-1]; got.Kind != "telegram" || !got.Literal["token"] || got.Values["token"] != "" {
		t.Errorf("an untouched literal secret is kept: %+v", got)
	}
}

func TestSettingsFormSavesDefaultsAndRetention(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = goTo(t, screen, secSettings, "general")
	screen = keys(t, screen, "e")
	if screen.form.kind != formSettings || screen.form.field("default_schedule").value != "daily 02:00" || screen.form.field("retention").counts[1] != "7" {
		t.Fatalf("form not prefilled: %+v", screen.form.fields)
	}
	screen.form.field("cooldown").value = "30m"
	screen.form.field("notify_success").value = "yes"
	screen.form.field("default_repositories").ticked[1] = true
	screen.form.field("retention").counts[2] = "4"
	keys(t, screen, "ctrl+s")
	if len(configFile.settings) != 1 {
		t.Fatalf("not saved: %v", screen.form.problems())
	}
	got := configFile.settings[0]
	if got.Cooldown != "30m" || !got.NotifySuccess || strings.Join(got.DefaultRepositories, ",") != "s3" || got.DefaultRetention.Daily != 7 || got.DefaultRetention.Weekly != 4 || got.ServerName != "web-01" {
		t.Errorf("saved settings = %+v", got)
	}

	bad, _ := configScreen(t)
	bad = goTo(t, bad, secSettings, "general")
	bad = keys(t, bad, "e")
	bad.form.field("cooldown").value = "often"
	bad.form.attempted = true
	if view := plain(bad.render()); !strings.Contains(view, "✗ cooldown") {
		t.Errorf("a bad cooldown is flagged:\n%s", view)
	}
}

func TestConfigTabJobsUseTheJobForm(t *testing.T) {
	screen, _ := configScreen(t)
	screen = goTo(t, screen, secJob, "appdb")
	screen = keys(t, screen, "e")
	if screen.mode != modeForm || screen.form.kind != formJob || screen.form.original != "appdb" {
		t.Errorf("mode %d kind %d original %q", screen.mode, screen.form.kind, screen.form.original)
	}
}

func TestConfigTabMouseSecondClickEditsAndAddsFromTheSection(t *testing.T) {
	screen, _ := configScreen(t)
	screen = goTo(t, screen, secUptime, "web")
	screen, _ = clickOn(t, screen, fmt.Sprintf("row:%d", screen.cursor)) // already selected: opens it
	if screen.mode != modeForm || screen.form.kind != formUptime || screen.form.original != "web" {
		t.Errorf("mode %d kind %d original %q", screen.mode, screen.form.kind, screen.form.original)
	}
}

func TestConfigTabWithoutAConfigFileSaysSo(t *testing.T) {
	model, _ := newManageModel(t.Context(), func() (status.Report, error) { return sampleReport(), nil }, manageActions{}, nil).Update(reportMsg{report: sampleReport()})
	screen := model.(manageModel)
	model, _ = screen.Update(tea5())
	if view := plain(model.(manageModel).render()); !strings.Contains(view, "5 Config") {
		t.Errorf("the tab is still there:\n%s", view)
	}
}

func tea5() tea.KeyPressMsg { return tea.KeyPressMsg{Code: '5', Text: "5"} }

func wideScreen() tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: 160, Height: 40} }

func TestTestKeySendsAMessageThroughTheSelectedNotifier(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = goTo(t, screen, secNotifier, "telegram")
	model, cmd := screen.onKey("t", "t")
	screen = model.(manageModel)
	if screen.mode != modeBusy || !strings.Contains(screen.busyText, "telegram") {
		t.Fatalf("mode %d busy %q: the test runs in the background", screen.mode, screen.busyText)
	}
	screen = feed(t, screen, messagesOf(cmd))
	if strings.Join(configFile.tested, ",") != "telegram" || screen.mode != modeTable || screen.noticeBad || !strings.Contains(screen.notice, "Đã gửi tin thử tới telegram") {
		t.Errorf("tested %v mode %d notice %q (bad %v)", configFile.tested, screen.mode, screen.notice, screen.noticeBad)
	}

	configFile.testErr = errors.New("telegram: 401 Unauthorized")
	model, cmd = screen.onKey("t", "t")
	screen = feed(t, model.(manageModel), messagesOf(cmd))
	if !screen.noticeBad || !strings.Contains(screen.notice, "thất bại") || !strings.Contains(screen.notice, "401") {
		t.Errorf("a failed test says why: %q (bad %v)", screen.notice, screen.noticeBad)
	}
}

func TestTestKeyNeedsANotifierLine(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = goTo(t, screen, secUptime, "web")
	model, cmd := screen.onKey("t", "t")
	screen = model.(manageModel)
	if cmd != nil || screen.mode != modeTable || len(configFile.tested) != 0 || !strings.Contains(screen.notice, "Notifier") {
		t.Errorf("t on an uptime line: mode %d tested %v notice %q", screen.mode, configFile.tested, screen.notice)
	}
	if view := plain(screen.render()); !strings.Contains(view, "gửi thử") {
		t.Errorf("the key bar offers the test:\n%s", view)
	}
}

func TestConfigTabGroupsEntriesByPurposeAndInvitesToFillAnEmptyGroup(t *testing.T) {
	screen, configFile := configScreen(t)
	configFile.overview.Notifiers = nil
	model, _ := screen.refreshConfig().Update(wideScreen())
	screen = model.(manageModel)

	view := plain(screen.render())
	last := -1
	for _, heading := range []string{"REPOSITORY", "JOB", "UPTIME CHECK", "NOTIFIER", "CÀI ĐẶT CHUNG"} {
		at := strings.Index(view, heading)
		if at <= last {
			t.Fatalf("group %q is missing or out of order:\n%s", heading, view)
		}
		last = at
	}
	if !strings.Contains(view, "+ chưa có nơi nhận cảnh báo nào: bấm a để thêm") {
		t.Errorf("an empty group invites adding its first entry:\n%s", view)
	}

	screen = goTo(t, screen, secNotifier, "")
	footer := plain(screen.render())
	if !strings.Contains(footer, "a thêm") || strings.Contains(footer, "e sửa") || strings.Contains(footer, "d xoá") {
		t.Errorf("on the invitation only a (add) makes sense:\n%s", footer)
	}
	if model, _ := screen.onKey("d", "d"); !strings.Contains(model.(manageModel).notice, "chưa có mục nào để xoá") {
		t.Errorf("d on the invitation: %q", model.(manageModel).notice)
	}
	if model, _ := screen.onKey("t", "t"); model.(manageModel).mode != modeTable || !strings.Contains(model.(manageModel).notice, "Chọn một dòng Notifier") {
		t.Errorf("t on the invitation sends nothing: %q", model.(manageModel).notice)
	}
	for _, key := range []string{"a", "e", "enter"} { // enter and e add too, as there is nothing to edit
		model, _ := screen.onKey(key, key)
		if model.(manageModel).mode != modeForm {
			t.Errorf("%s on the invitation should open the form to add (mode %d)", key, model.(manageModel).mode)
		}
	}
}

func TestConfigKeyHintsFollowTheSelectedEntry(t *testing.T) {
	screen, _ := configScreen(t)
	model, _ := screen.Update(wideScreen())
	screen = model.(manageModel)
	for _, tc := range []struct {
		section     configSection
		name        string
		want, avoid []string
	}{
		{secNotifier, "telegram", []string{"a thêm", "e sửa", "d xoá", "t gửi thử"}, nil},
		{secRepository, "local", []string{"a thêm", "e sửa", "d xoá"}, []string{"t gửi thử"}},
		{secSettings, "general", []string{"e sửa"}, []string{"a thêm", "d xoá", "t gửi thử"}},
	} {
		view := plain(goTo(t, screen, tc.section, tc.name).render())
		for _, want := range tc.want {
			if !strings.Contains(view, want) {
				t.Errorf("%s %s: no %q hint", sectionNames[tc.section], tc.name, want)
			}
		}
		for _, avoid := range tc.avoid {
			if strings.Contains(view, avoid) {
				t.Errorf("%s %s: %q does nothing here and must not be offered", sectionNames[tc.section], tc.name, avoid)
			}
		}
	}
}

func TestConfigDetailShowsTheJobSetupAsLabelsAndValues(t *testing.T) {
	screen, _ := configScreen(t)
	model, _ := screen.Update(wideScreen())
	view := plain(goTo(t, model.(manageModel), secJob, "appdb").render())
	for _, want := range []string{"Job · appdb", "Loại", "postgres", "Repository", "Lịch", "Lần tới", "Giữ", "daily 7"} {
		if !strings.Contains(view, want) {
			t.Errorf("the detail of a job lacks %q:\n%s", want, view)
		}
	}
}
