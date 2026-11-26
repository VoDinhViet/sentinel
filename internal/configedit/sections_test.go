package configedit

import (
	"errors"
	"os"
	"sentinel/internal/notify"
	"strings"
	"testing"

	"sentinel/internal/config"
)

const sectionsConfig = `# kept
server_name: web-01    # the name in alerts
store: /var/lib/sentinel/state.db
notifiers:
  telegram: { token: "${TG_TOKEN}", chat_id: "-100123" }
  smtp:
    host: smtp.example.com
    pass: hunter2
    to: [ops@example.com, dev@example.com]
repositories:
  s3:
    url: "s3:http://x/bucket"   # main repo
    password: "${RESTIC_PW}"
    env:
      AWS_ACCESS_KEY_ID: "${AWS_ID}"
      AWS_REGION: eu-west-1
  local:
    url: /var/backups/repo
    password: literal-secret
jobs:
  - name: site
    type: files
    paths: [/srv/www]
    repositories: [s3]
uptime:
  - { name: web, type: http, url: "https://example.com", expect_status: 200 }
  - { name: db, type: tcp, address: "10.0.0.5:5432" }
`

func TestReadShowsTheNonJobParts(t *testing.T) {
	overview, err := Read(writeConfig(t, sectionsConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Repositories) != 2 || overview.Repositories[0].Name != "s3" || overview.Repositories[0].PasswordEnv != "RESTIC_PW" ||
		overview.Repositories[0].Env["AWS_ACCESS_KEY_ID"] != "AWS_ID" || len(overview.Repositories[0].Env) != 1 ||
		strings.Join(overview.Repositories[0].UsedBy, ",") != "site" {
		t.Errorf("s3 = %+v", overview.Repositories[0])
	}
	if local := overview.Repositories[1]; !local.PasswordLiteral || local.PasswordEnv != "" || len(local.UsedBy) != 0 {
		t.Errorf("local = %+v (a literal password is flagged, never shown)", local)
	}
	if len(overview.Uptime) != 2 || overview.Uptime[0].URL != "https://example.com" || overview.Uptime[0].ExpectStatus != "200" || overview.Uptime[1].Address != "10.0.0.5:5432" {
		t.Errorf("uptime = %+v", overview.Uptime)
	}
	if len(overview.Notifiers) != 2 || overview.Notifiers[0].Kind != "telegram" || overview.Notifiers[0].Values["token"] != "TG_TOKEN" || overview.Notifiers[0].Values["chat_id"] != "-100123" {
		t.Errorf("notifiers = %+v", overview.Notifiers)
	}
	if smtp := overview.Notifiers[1]; !smtp.Literal["pass"] || smtp.Values["pass"] != "" || smtp.Values["to"] != "ops@example.com, dev@example.com" {
		t.Errorf("smtp = %+v (the literal password is flagged, never shown)", smtp)
	}
	if overview.Settings.ServerName != "web-01" || overview.Settings.Store != "/var/lib/sentinel/state.db" || overview.Settings.Cooldown != "" {
		t.Errorf("settings = %+v (only what the file says, no defaults)", overview.Settings)
	}
}

func TestSaveRepositoryAddsEditsAndKeepsEverythingElse(t *testing.T) {
	path := writeConfig(t, sectionsConfig)
	add := Repository{Name: "offsite", URL: "s3:https://minio.example.com/b", PasswordEnv: "RESTIC_PW", Env: map[string]string{"AWS_SECRET_ACCESS_KEY": "MINIO_SECRET", "AWS_ACCESS_KEY_ID": "MINIO_ID"}}
	if err := SaveRepository(path, "", add); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, path)
	for _, want := range []string{"# kept", "# the name in alerts", "# main repo", "offsite:", `"${MINIO_ID}"`, `"${MINIO_SECRET}"`, "password: literal-secret", "AWS_REGION: eu-west-1"} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved config lacks %q:\n%s", want, saved)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("permissions changed: %v %v", info, err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Error("the previous file must be kept as .bak")
	}

	// Editing: the literal password stays unless a variable is named; an unwanted env reference goes, a literal one stays.
	edit := Repository{Name: "s3", URL: "s3:http://x/other", PasswordEnv: "", PasswordLiteral: false, Env: map[string]string{}}
	if err := SaveRepository(path, "s3", edit); err == nil {
		t.Error("a repository needs a password variable")
	}
	edit.PasswordEnv = "NEW_PW"
	if err := SaveRepository(path, "s3", edit); err != nil {
		t.Fatal(err)
	}
	saved = readFile(t, path)
	if !strings.Contains(saved, `"${NEW_PW}"`) || strings.Contains(saved, "AWS_ID") || !strings.Contains(saved, "AWS_REGION: eu-west-1") {
		t.Errorf("after editing s3:\n%s", saved)
	}

	var invalid *InvalidError
	if err := SaveRepository(path, "s3", Repository{Name: "renamed", URL: "x", PasswordEnv: "P"}); !errors.As(err, &invalid) {
		t.Errorf("renaming = %v, want InvalidError", err)
	}
	if err := SaveRepository(path, "", Repository{Name: "s3", URL: "x", PasswordEnv: "P"}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate = %v, want ErrExists", err)
	}
	if err := SaveRepository(path, "nope", Repository{Name: "nope", URL: "x", PasswordEnv: "P"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing = %v, want ErrNotFound", err)
	}
	if err := SaveRepository(path, "", Repository{Name: "bad name", URL: "x", PasswordEnv: "P"}); !errors.As(err, &invalid) || invalid.Field != "name" {
		t.Errorf("bad name = %v", err)
	}
	if err := SaveRepository(path, "", Repository{Name: "ok", URL: "x", PasswordEnv: "hunter 2"}); !errors.As(err, &invalid) || invalid.Field != "password_env" {
		t.Errorf("a password typed as a variable name = %v", err)
	}
}

func TestDeleteRepositoryRefusesOneThatIsInUse(t *testing.T) {
	path := writeConfig(t, sectionsConfig)
	var invalid *InvalidError
	if err := DeleteRepository(path, "s3"); !errors.As(err, &invalid) || !strings.Contains(err.Error(), "site") {
		t.Errorf("deleting a used repository = %v, want a refusal naming the job", err)
	}
	if err := DeleteRepository(path, "local"); err != nil {
		t.Fatal(err)
	}
	if after := readFile(t, path); strings.Contains(after, "var/backups/repo") || !strings.Contains(after, "s3:") {
		t.Errorf("after delete:\n%s", after)
	}
	if err := DeleteRepository(path, "local"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice = %v", err)
	}
}

func TestUptimeChecksAreAddedEditedAndRemoved(t *testing.T) {
	path := writeConfig(t, sectionsConfig)
	check := Uptime{Name: "api", Type: "http", URL: "https://api.example.com/health", Interval: "30s", ExpectStatus: "204", Keyword: "ok", CertExpiryDays: "14", Host: "ignored", Address: "ignored"}
	if err := SaveUptime(path, "", check); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, path)
	if !strings.Contains(saved, "name: api") || !strings.Contains(saved, "interval: 30s") || !strings.Contains(saved, "expect_status: 204") || strings.Contains(saved, "ignored") {
		t.Errorf("saved:\n%s", saved)
	}
	check.Type, check.Address, check.URL = "tcp", "db.example.com:5432", "https://ignored"
	if err := SaveUptime(path, "api", check); err != nil {
		t.Fatal(err)
	}
	saved = readFile(t, path)
	if strings.Contains(saved, "keyword") || strings.Contains(saved, "api.example.com") || !strings.Contains(saved, "address: db.example.com:5432") {
		t.Errorf("a tcp check keeps only what tcp uses:\n%s", saved)
	}
	var invalid *InvalidError
	for field, bad := range map[string]Uptime{
		"name":       {Name: "a b", Type: "http", URL: "https://x"},
		"type":       {Name: "x", Type: "smtp"},
		"url":        {Name: "x", Type: "http", URL: "ftp://x"},
		"address":    {Name: "x", Type: "tcp", Address: "nohost"},
		"host":       {Name: "x", Type: "ping"},
		"interval":   {Name: "x", Type: "ping", Host: "h", Interval: "soon"},
		"fail_after": {Name: "x", Type: "ping", Host: "h", FailAfter: "-2"},
	} {
		if err := SaveUptime(path, "", bad); !errors.As(err, &invalid) || invalid.Field != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}
	if err := SaveUptime(path, "", Uptime{Name: "web", Type: "ping", Host: "h"}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate = %v", err)
	}
	for _, name := range []string{"api", "web", "db"} {
		if err := DeleteUptime(path, name); err != nil {
			t.Fatal(err)
		}
	}
	if after := readFile(t, path); strings.Contains(after, "uptime") {
		t.Errorf("the empty uptime list is dropped:\n%s", after)
	}
	if err := DeleteUptime(path, "web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice = %v", err)
	}
}

func TestNotifiersKeepSecretsAsVariableNames(t *testing.T) {
	path := writeConfig(t, sectionsConfig)
	if err := SaveNotifier(path, Notifier{Kind: "discord", Values: map[string]string{"webhook": "DISCORD_HOOK"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveNotifier(path, Notifier{Kind: "telegram", Values: map[string]string{"token": "TG2", "chat_id": "-999"}}); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, path)
	for _, want := range []string{`webhook: "${DISCORD_HOOK}"`, `"${TG2}"`, `chat_id: "-999"`, "password"} {
		if want != "password" && !strings.Contains(saved, want) {
			t.Errorf("saved lacks %q:\n%s", want, saved)
		}
	}
	// The smtp literal password stays when no variable is named, and goes when one is.
	smtp := Notifier{Kind: "smtp", Values: map[string]string{"host": "smtp.example.com", "port": "465", "to": "a@x.com, b@x.com"}, Literal: map[string]bool{"pass": true}}
	if err := SaveNotifier(path, smtp); err != nil {
		t.Fatal(err)
	}
	if saved = readFile(t, path); !strings.Contains(saved, "pass: hunter2") || !strings.Contains(saved, "port: 465") || !strings.Contains(saved, "[a@x.com, b@x.com]") {
		t.Errorf("smtp:\n%s", saved)
	}
	smtp.Values["pass"], smtp.Literal = "SMTP_PASS", nil
	if err := SaveNotifier(path, smtp); err != nil {
		t.Fatal(err)
	}
	if saved = readFile(t, path); strings.Contains(saved, "hunter2") || !strings.Contains(saved, `"${SMTP_PASS}"`) {
		t.Errorf("a named variable replaces the literal:\n%s", saved)
	}

	var invalid *InvalidError
	for field, bad := range map[string]Notifier{
		"token":   {Kind: "telegram", Values: map[string]string{"chat_id": "1"}},
		"webhook": {Kind: "slack", Values: map[string]string{"webhook": "https://hooks.slack.com/real-secret"}},
		"to":      {Kind: "smtp", Values: map[string]string{"host": "h"}},
		"port":    {Kind: "smtp", Values: map[string]string{"host": "h", "to": "a@x", "port": "99999"}},
		"kind":    {Kind: "pigeon"},
	} {
		if err := SaveNotifier(path, bad); !errors.As(err, &invalid) || invalid.Field != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}
	for _, kind := range notify.Kinds() {
		if err := DeleteNotifier(path, kind); err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if after := readFile(t, path); strings.Contains(after, "notifiers") {
		t.Errorf("the empty notifiers block is dropped:\n%s", after)
	}
}

func TestSettingsWriteOnlyWhatIsSet(t *testing.T) {
	path := writeConfig(t, sectionsConfig)
	settings := Settings{
		ServerName: "web-02", Store: "/var/lib/sentinel/state.db", Cooldown: "30m", NotifySuccess: true,
		VerifySchedule: "weekly sun 04:00", PruneSchedule: "daily 05:00", ShutdownWait: "90m", DefaultSchedule: "daily 02:00", DefaultRepositories: []string{"s3"},
		DefaultRetention: config.Retention{Daily: 7, Weekly: 4},
	}
	if err := SaveSettings(path, settings); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, path)
	for _, want := range []string{"server_name: web-02", "# the name in alerts", "cooldown: 30m", "shutdown_wait: 90m", "notify_success: true", "verify_schedule: weekly sun 04:00", "prune_schedule: daily 05:00", "schedule: daily 02:00", "repositories: [s3]", "daily: 7"} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved lacks %q:\n%s", want, saved)
		}
	}
	overview, err := Read(path)
	if err != nil || overview.Settings.PruneSchedule != "daily 05:00" || overview.Settings.DefaultSchedule != "daily 02:00" || overview.Settings.DefaultRetention.Weekly != 4 || !overview.Settings.NotifySuccess || overview.Settings.Cooldown != "30m" {
		t.Errorf("read back: %+v %v", overview.Settings, err)
	}

	if err := SaveSettings(path, Settings{ServerName: "web-02"}); err != nil {
		t.Fatal(err)
	}
	cleared := readFile(t, path)
	for _, gone := range []string{"alert:", "defaults:", "cooldown", "verify_schedule", "prune_schedule", "store:"} {
		if strings.Contains(cleared, gone) {
			t.Errorf("an empty value removes %q:\n%s", gone, cleared)
		}
	}
	var invalid *InvalidError
	if err := SaveSettings(path, Settings{ShutdownWait: "soon"}); !errors.As(err, &invalid) || invalid.Field != "shutdown_wait" {
		t.Errorf("bad shutdown_wait = %v", err)
	}
	if err := SaveSettings(path, Settings{Cooldown: "often"}); !errors.As(err, &invalid) || invalid.Field != "cooldown" {
		t.Errorf("bad cooldown = %v", err)
	}
	if err := SaveSettings(path, Settings{DefaultRepositories: []string{"nope"}}); !errors.As(err, &invalid) {
		t.Errorf("unknown default repository = %v, want InvalidError from the config check", err)
	}
	if err := SaveSettings(path, Settings{VerifySchedule: "weekly someday 04:00"}); !errors.As(err, &invalid) {
		t.Errorf("bad schedule = %v", err)
	}
	if err := SaveSettings(path, Settings{PruneSchedule: "weekly someday 04:00"}); !errors.As(err, &invalid) || invalid.Field != "prune_schedule" {
		t.Errorf("bad prune schedule = %v", err)
	}
}

func TestResourceSettingsAreWrittenAndRemovedWhenEmpty(t *testing.T) {
	path := writeConfig(t, sectionsConfig)
	if err := SaveSettings(path, Settings{Priority: "normal", MaxParallel: "2", UploadLimitKiB: "20480"}); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, path)
	for _, want := range []string{"resources:", "priority: normal", "max_parallel: 2", "upload_limit_kib: 20480"} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved lacks %q:\n%s", want, saved)
		}
	}
	overview, err := Read(path)
	if err != nil || overview.Settings.Priority != "normal" || overview.Settings.MaxParallel != "2" || overview.Settings.UploadLimitKiB != "20480" {
		t.Errorf("read back: %+v %v", overview.Settings, err)
	}
	if err := SaveSettings(path, Settings{}); err != nil {
		t.Fatal(err)
	}
	if cleared := readFile(t, path); strings.Contains(cleared, "resources:") {
		t.Errorf("empty values remove the block:\n%s", cleared)
	}
	var invalid *InvalidError
	for field, settings := range map[string]Settings{
		"priority": {Priority: "turbo"}, "max_parallel": {MaxParallel: "0"}, "upload_limit_kib": {UploadLimitKiB: "fast"},
	} {
		if err := SaveSettings(path, settings); !errors.As(err, &invalid) || invalid.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
}

// The editor knows nothing about the disk alert; saving the settings must not drop it from the file.
func TestSaveSettingsKeepsTheDiskBlock(t *testing.T) {
	path := writeConfig(t, sectionsConfig+"\ndisk:\n  min_free_percent: 20\n  paths: [/data]\n")
	if err := SaveSettings(path, Settings{ServerName: "web-02", Cooldown: "30m"}); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, path)
	for _, want := range []string{"disk:", "min_free_percent: 20", "paths: [/data]"} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved lacks %q:\n%s", want, saved)
		}
	}
}
