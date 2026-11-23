package configedit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/config"
)

const baseConfig = `# managed by hand
repositories:
  s3:
    url: "s3:http://x/bucket"   # main repo
    password: "${RESTIC_PW}"
  local:
    url: /var/backups/repo
    password: "${RESTIC_PW}"
backups:
  - name: site
    type: files
    paths: [/srv/www]
    repositories: [s3]
  - name: legacy        # old database
    type: postgres
    timeout: 2h
    postgres: { host: db.internal, port: 5433, user: backup, password: hunter2, database: legacy }
    repositories: [local]
`

func writeConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestListShowsAllJobsAndFlagsLiteralPasswords(t *testing.T) {
	listing, err := List(writeConfig(t, baseConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Jobs) != 2 || listing.Jobs[0].Editable() || !listing.Jobs[1].Editable() {
		t.Fatalf("jobs = %+v (the files job is listed but not editable)", listing.Jobs)
	}
	legacy := listing.Jobs[1]
	if legacy.Port != 5433 || legacy.PasswordEnv != "" || !legacy.PasswordLiteral {
		t.Errorf("legacy = %+v", legacy)
	}
	if strings.Join(listing.Repositories, ",") != "local,s3" {
		t.Errorf("repositories = %v", listing.Repositories)
	}
}

func TestSaveAddsAJobAndKeepsEverythingElse(t *testing.T) {
	path := writeConfig(t, baseConfig)
	job := Job{Name: "shop", Type: config.BackupMySQL, Database: "shop", User: "dump", PasswordEnv: "MYSQL_SHOP_PW",
		Schedule: "30 2 * * *", Repositories: []string{"s3", "local"}, TLS: "disabled",
		Retention: config.Retention{Daily: 7, Weekly: 4}}
	if err := Save(path, "", job); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	for _, want := range []string{"# managed by hand", "# main repo", "# old database", "hunter2", "name: shop", "${MYSQL_SHOP_PW}", "tls: disabled", `"30 2 * * *"`, "daily: 7"} {
		if !strings.Contains(text, want) {
			t.Errorf("config lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "MYSQL_SHOP_PW}\"\n") && strings.Contains(text, "password: shop") {
		t.Error("the password must be a reference only")
	}
	listing, err := List(path)
	if err != nil || len(listing.Jobs) != 3 {
		t.Fatalf("after save: %+v, %v", listing, err)
	}
	shop := listing.Jobs[2]
	if shop.Host != "localhost" || shop.Port != 3306 || shop.PasswordEnv != "MYSQL_SHOP_PW" || strings.Join(shop.Repositories, ",") != "local,s3" {
		t.Errorf("shop = %+v (defaults for host and port are filled in)", shop)
	}
}

func TestSaveKeepsPermissionsAndLeavesABackup(t *testing.T) {
	path := writeConfig(t, baseConfig)
	if err := Save(path, "", Job{Name: "a", Type: config.BackupPostgres, Database: "a", Repositories: []string{"s3"}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info.Mode().Perm(), err)
	}
	if got := readFile(t, path+".bak"); got != baseConfig {
		t.Errorf("the backup must hold the previous file, got:\n%s", got)
	}
	backupInfo, _ := os.Stat(path + ".bak")
	if backupInfo.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600 like the config", backupInfo.Mode().Perm())
	}
}

func TestEditingAJobKeepsItsLiteralPasswordUnlessReplaced(t *testing.T) {
	path := writeConfig(t, baseConfig)
	listing, _ := List(path)
	job := listing.Jobs[1]
	job.Schedule = "0 3 * * *"
	if err := Save(path, "legacy", job); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	if !strings.Contains(text, "hunter2") || !strings.Contains(text, "timeout: 2h") || !strings.Contains(text, "# old database") {
		t.Errorf("an edit must keep the literal password, other keys and comments:\n%s", text)
	}
	job.PasswordEnv = "PG_LEGACY_PW"
	if err := Save(path, "legacy", job); err != nil {
		t.Fatal(err)
	}
	if text := readFile(t, path); strings.Contains(text, "hunter2") || !strings.Contains(text, "${PG_LEGACY_PW}") {
		t.Errorf("a new reference replaces the literal password:\n%s", text)
	}
}

func TestRenameAndDuplicateNames(t *testing.T) {
	path := writeConfig(t, baseConfig)
	listing, _ := List(path)
	renamed := listing.Jobs[1]
	renamed.Name = "legacy2"
	if err := Save(path, "legacy", renamed); err != nil {
		t.Fatal(err)
	}
	renamed.Name = "site" // taken by the files job
	if err := Save(path, "legacy2", renamed); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate name error = %v, want ErrExists", err)
	}
	if err := Save(path, "", Job{Name: "legacy2", Type: config.BackupPostgres, Database: "x", Repositories: []string{"s3"}}); !errors.Is(err, ErrExists) {
		t.Errorf("adding an existing name = %v, want ErrExists", err)
	}
	if err := Save(path, "missing", Job{Name: "m", Type: config.BackupPostgres, Database: "x", Repositories: []string{"s3"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("editing a missing job = %v, want ErrNotFound", err)
	}
}

func TestFileJobsAndTypeChangesAreRefused(t *testing.T) {
	path := writeConfig(t, baseConfig)
	var invalidErr *InvalidError
	if err := Save(path, "site", Job{Name: "site", Type: config.BackupPostgres, Database: "x", Repositories: []string{"s3"}}); !errors.As(err, &invalidErr) {
		t.Errorf("editing a files job = %v, want InvalidError", err)
	}
	if err := Save(path, "legacy", Job{Name: "legacy", Type: config.BackupMongoDB, Database: "x", Repositories: []string{"s3"}}); !errors.As(err, &invalidErr) {
		t.Errorf("changing the type = %v, want InvalidError", err)
	}
	if err := Delete(path, "site"); !errors.As(err, &invalidErr) {
		t.Errorf("deleting a files job = %v, want InvalidError", err)
	}
	if readFile(t, path) != baseConfig {
		t.Error("a refused change must not touch the file")
	}
}

func TestValidate(t *testing.T) {
	repositories := []string{"s3", "local"}
	good := func() Job {
		return Job{Name: "db", Type: config.BackupPostgres, Database: "app", Repositories: []string{"s3"}}
	}
	for name, change := range map[string]func(*Job){
		"bad name":         func(j *Job) { j.Name = "a,b" },
		"empty name":       func(j *Job) { j.Name = "" },
		"no database":      func(j *Job) { j.Database = "" },
		"bad host":         func(j *Job) { j.Host = "db host" },
		"port too high":    func(j *Job) { j.Port = 70000 },
		"password literal": func(j *Job) { j.PasswordEnv = "hunter2!" },
		"bad cron":         func(j *Job) { j.Schedule = "every day" },
		"no repository":    func(j *Job) { j.Repositories = nil },
		"unknown repo":     func(j *Job) { j.Repositories = []string{"nope"} },
		"negative":         func(j *Job) { j.Retention.Daily = -1 },
		"unknown type":     func(j *Job) { j.Type = "oracle" },
		"bad mysql tls":    func(j *Job) { j.Type, j.TLS = config.BackupMySQL, "maybe" },
	} {
		job := good()
		change(&job)
		var invalidErr *InvalidError
		if err := Validate(&job, repositories); !errors.As(err, &invalidErr) {
			t.Errorf("%s: err = %v, want InvalidError", name, err)
		}
	}
	job := good()
	if err := Validate(&job, repositories); err != nil || job.Host != "localhost" || job.Port != 5432 {
		t.Errorf("good job: %+v, %v", job, err)
	}
	mongo := good()
	mongo.Type, mongo.AuthDatabase, mongo.TLS = config.BackupMongoDB, "admin", "required"
	if err := Validate(&mongo, repositories); err != nil || mongo.Port != 27017 || mongo.TLS != "" {
		t.Errorf("mongo: %+v, %v (port defaults, tls is mysql-only)", mongo, err)
	}
}

func TestDeleteRemovesTheJobAndDropsAnEmptyBackupsList(t *testing.T) {
	path := writeConfig(t, baseConfig)
	if err := Delete(path, "legacy"); err != nil {
		t.Fatal(err)
	}
	text := readFile(t, path)
	if strings.Contains(text, "legacy") || !strings.Contains(text, "name: site") || !strings.Contains(text, "# main repo") {
		t.Errorf("after delete:\n%s", text)
	}
	if err := Delete(path, "legacy"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice = %v, want ErrNotFound", err)
	}

	only := writeConfig(t, "repositories:\n  s3: { url: \"s3:http://x/b\", password: x }\nbackups:\n  - name: db\n    type: postgres\n    postgres: { database: app }\n    repositories: [s3]\n")
	if err := Delete(only, "db"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, only), "backups") {
		t.Error("an empty backups list is dropped")
	}
}

func TestBrokenConfigIsNotEdited(t *testing.T) {
	path := writeConfig(t, "backups: [oops\n")
	if err := Save(path, "", Job{Name: "a", Type: config.BackupPostgres, Database: "a", Repositories: []string{"s3"}}); err == nil {
		t.Fatal("want an error for a config that does not parse")
	}
	if readFile(t, path) != "backups: [oops\n" {
		t.Error("a broken config must be left alone")
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("no backup is made when nothing was written")
	}
}
