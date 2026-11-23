package main

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"strings"
	"testing"

	"sentinel/internal/status"
)

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
		"5 Config", "Repository", "local", "/var/backups/repo", "password ${RESTIC_PW}", "not used by any job",
		"written in the file", "used by appdb",
		"Job", "appdb", "postgres", "→ s3", "keep daily 7",
		"Uptime", "web", "http https://example.com", "every 30s",
		"Notifier", "telegram", "token ${TG_TOKEN}", "chat_id -100",
		"Settings", "server web-01", "defaults: daily 02:00",
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
	if screen.mode != modeForm || screen.form.kind != formRepository || screen.form.title != "Add a repository" {
		t.Fatalf("mode %d form %+v", screen.mode, screen.form.title)
	}
	screen = keys(t, screen, "tab", "right") // kind: local -> s3
	if view := plain(screen.render()); !strings.Contains(view, "Bucket") || strings.Contains(view, "Folder  ") && strings.Contains(view, "rclone remote") {
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
	if screen.mode != modeTable || !strings.Contains(screen.notice, "Added repository offsite") {
		t.Errorf("mode %d notice %q", screen.mode, screen.notice)
	}
}

func TestRepositoryFormShowsEveryMissingPartAtOnce(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = keys(t, screen, "a")
	screen.form.field("password_env").value = ""
	screen = keys(t, screen, "ctrl+s")
	view := plain(screen.render())
	for _, want := range []string{"✗ invalid repository name", "✗ folder is required", "✗ name the environment variable"} {
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
	if screen.mode != modeTable || !strings.Contains(screen.notice, "used by appdb") || len(configFile.removed) != 0 {
		t.Errorf("mode %d notice %q", screen.mode, screen.notice)
	}
	screen = goTo(t, screen, secRepository, "local")
	screen = keys(t, screen, "d")
	if screen.mode != modeDelete {
		t.Fatalf("mode %d: %s", screen.mode, screen.notice)
	}
	screen = typeText(t, screen, "local")
	screen = keys(t, screen, "enter")
	if len(configFile.removed) != 1 || configFile.removed[0] != "repository local" || screen.mode != modeTable || !strings.Contains(screen.notice, "Removed repository local") {
		t.Errorf("removed %v mode %d notice %q", configFile.removed, screen.mode, screen.notice)
	}
}

func TestUptimeFormHidesWhatTheTypeDoesNotUse(t *testing.T) {
	screen, configFile := configScreen(t)
	screen = goTo(t, screen, secUptime, "web")
	screen = keys(t, screen, "a")
	view := plain(screen.render())
	if !strings.Contains(view, "Address") || strings.Contains(view, "Host:port") || !strings.Contains(view, "Expect status") {
		t.Errorf("http fields:\n%s", view)
	}
	screen = keys(t, screen, "tab", "right") // type: http -> tcp
	view = plain(screen.render())
	if !strings.Contains(view, "Host:port") || strings.Contains(view, "Expect status") || strings.Contains(view, "Must contain") {
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
	if screen.mode != modeForm || !strings.Contains(plain(screen.render()), "✗ to is required") {
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
