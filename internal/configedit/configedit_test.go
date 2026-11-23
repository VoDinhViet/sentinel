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
jobs:
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
	if len(listing.Jobs) != 2 || !listing.Jobs[0].Editable() || !listing.Jobs[1].Editable() {
		t.Fatalf("jobs = %+v (files and database jobs are editable)", listing.Jobs)
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
	job := Job{Name: "shop", Type: config.JobMySQL, Database: "shop", User: "dump", PasswordEnv: "MYSQL_SHOP_PW",
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

func TestJobTimeoutIsWrittenReadBackAndRemovedWhenEmpty(t *testing.T) {
	path := writeConfig(t, baseConfig)
	job := Job{Name: "big", Type: config.JobPostgres, Database: "big", Repositories: []string{"s3"}, Timeout: "36h"}
	if err := Save(path, "", job); err != nil {
		t.Fatal(err)
	}
	if text := readFile(t, path); !strings.Contains(text, "timeout: 36h") {
		t.Fatalf("timeout not written:\n%s", text)
	}
	listing, err := List(path)
	if err != nil || listing.Jobs[len(listing.Jobs)-1].Timeout != "36h0m0s" {
		t.Fatalf("read back: %+v %v", listing.Jobs, err)
	}
	if untouched := listing.Jobs[0]; untouched.Timeout != "" {
		t.Errorf("a job without a timeout shows the default as empty, got %q", untouched.Timeout)
	}
	job.Timeout = ""
	if err := Save(path, "big", job); err != nil {
		t.Fatal(err)
	}
	if text := readFile(t, path); strings.Contains(text, "timeout: 36h") {
		t.Errorf("an empty timeout removes the key:\n%s", text)
	}
}

func TestSaveKeepsPermissionsAndLeavesABackup(t *testing.T) {
	path := writeConfig(t, baseConfig)
	if err := Save(path, "", Job{Name: "a", Type: config.JobPostgres, Database: "a", Repositories: []string{"s3"}}); err != nil {
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
	if err := Save(path, "", Job{Name: "legacy2", Type: config.JobPostgres, Database: "x", Repositories: []string{"s3"}}); !errors.Is(err, ErrExists) {
		t.Errorf("adding an existing name = %v, want ErrExists", err)
	}
	if err := Save(path, "missing", Job{Name: "m", Type: config.JobPostgres, Database: "x", Repositories: []string{"s3"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("editing a missing job = %v, want ErrNotFound", err)
	}
}

func TestTypeChangesAreRefused(t *testing.T) {
	path := writeConfig(t, baseConfig)
	var invalidErr *InvalidError
	if err := Save(path, "site", Job{Name: "site", Type: config.JobPostgres, Database: "x", Repositories: []string{"s3"}}); !errors.As(err, &invalidErr) {
		t.Errorf("turning a files job into a database job = %v, want InvalidError", err)
	}
	if err := Save(path, "legacy", Job{Name: "legacy", Type: config.JobFiles, Paths: []string{"/x"}, Repositories: []string{"s3"}}); !errors.As(err, &invalidErr) {
		t.Errorf("changing the type = %v, want InvalidError", err)
	}
	if readFile(t, path) != baseConfig {
		t.Error("a refused change must not touch the file")
	}
}

func TestFilesJobsAreListedEditedAndRemoved(t *testing.T) {
	path := writeConfig(t, baseConfig)
	listing, err := List(path)
	if err != nil {
		t.Fatal(err)
	}
	site := listing.Jobs[0]
	if !site.Editable() || strings.Join(site.Paths, ",") != "/srv/www" {
		t.Fatalf("files job as listed: %+v", site)
	}
	site.Paths = []string{"/srv/www", "/etc/nginx"}
	site.Exclude = []string{"*.tmp"}
	site.Schedule = "weekly sun 03:00"
	site.Retention = config.Retention{Daily: 3}
	if err := Save(path, "site", site); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, path)
	for _, want := range []string{"# managed by hand", "# old database", "paths: [/srv/www, /etc/nginx]", `exclude: ['*.tmp']`, `schedule: "weekly sun 03:00"`, "retention: {daily: 3}", "password: hunter2"} {
		if !strings.Contains(saved, want) {
			t.Errorf("saved config lacks %q:\n%s", want, saved)
		}
	}
	site.Exclude = nil
	if err := Save(path, "site", site); err != nil || strings.Contains(readFile(t, path), "exclude") {
		t.Errorf("clearing exclude removes the key: %v\n%s", err, readFile(t, path))
	}

	fresh := Job{Name: "photos", Type: config.JobFiles, Paths: []string{"/home/me/Pictures"}, Repositories: []string{"local"}}
	if err := Save(path, "", fresh); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, "", Job{Name: "empty", Type: config.JobFiles, Repositories: []string{"local"}}); err == nil {
		t.Error("a files job needs a path")
	}
	if err := Delete(path, "site"); err != nil {
		t.Fatal(err)
	}
	if after := readFile(t, path); strings.Contains(after, "name: site") || !strings.Contains(after, "name: photos") {
		t.Errorf("after delete:\n%s", after)
	}
}

func TestValidate(t *testing.T) {
	repositories := []string{"s3", "local"}
	good := func() Job {
		return Job{Name: "db", Type: config.JobPostgres, Database: "app", Repositories: []string{"s3"}}
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
		"bad mysql tls":    func(j *Job) { j.Type, j.TLS = config.JobMySQL, "maybe" },
		"bad timeout":      func(j *Job) { j.Timeout = "forever" },
		"zero timeout":     func(j *Job) { j.Timeout = "0s" },
		"files no path":    func(j *Job) { j.Type, j.Database = config.JobFiles, "" },
		"files line break": func(j *Job) { j.Type, j.Paths = config.JobFiles, []string{"/a\nb"} },
	} {
		job := good()
		change(&job)
		var invalidErr *InvalidError
		if err := Validate(&job, Known{Repositories: repositories}); !errors.As(err, &invalidErr) {
			t.Errorf("%s: err = %v, want InvalidError", name, err)
		}
	}
	job := good()
	if err := Validate(&job, Known{Repositories: repositories}); err != nil || job.Host != "localhost" || job.Port != 5432 {
		t.Errorf("good job: %+v, %v", job, err)
	}
	mongo := good()
	mongo.Type, mongo.AuthDatabase, mongo.TLS = config.JobMongoDB, "admin", "required"
	if err := Validate(&mongo, Known{Repositories: repositories}); err != nil || mongo.Port != 27017 || mongo.TLS != "" {
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

	only := writeConfig(t, "repositories:\n  s3: { url: \"s3:http://x/b\", password: x }\njobs:\n  - name: db\n    type: postgres\n    postgres: { database: app }\n    repositories: [s3]\n")
	if err := Delete(only, "db"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, only), "jobs") {
		t.Error("an empty jobs list is dropped")
	}
}

func TestBrokenConfigIsNotEdited(t *testing.T) {
	path := writeConfig(t, "jobs: [oops\n")
	if err := Save(path, "", Job{Name: "a", Type: config.JobPostgres, Database: "a", Repositories: []string{"s3"}}); err == nil {
		t.Fatal("want an error for a config that does not parse")
	}
	if readFile(t, path) != "jobs: [oops\n" {
		t.Error("a broken config must be left alone")
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("no backup is made when nothing was written")
	}
}

func TestValidateNamesTheFieldAtFault(t *testing.T) {
	known := Known{Repositories: []string{"s3"}}
	for field, change := range map[string]func(*Job){
		"name":         func(j *Job) { j.Name = "a b" },
		"database":     func(j *Job) { j.Database = "" },
		"port":         func(j *Job) { j.Port = 70000 },
		"password_env": func(j *Job) { j.PasswordEnv = "no good" },
		"schedule":     func(j *Job) { j.Schedule = "daily 25:00" },
		"repositories": func(j *Job) { j.Repositories = nil },
		"retention":    func(j *Job) { j.Retention.Weekly = -2 },
	} {
		job := Job{Name: "db", Type: config.JobPostgres, Database: "app", Repositories: []string{"s3"}}
		change(&job)
		var invalidErr *InvalidError
		if err := Validate(&job, known); !errors.As(err, &invalidErr) || invalidErr.Field != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}
}

const defaultsConfig = `
repositories:
  only: { url: /tmp/r, password: p }
defaults:
  schedule: daily 02:00
  retention: { daily: 7 }
jobs:
  - name: inherits
    type: postgres
    postgres: { host: db, port: 5432, database: app }
`

func TestInheritedValuesStayOutOfTheFile(t *testing.T) {
	path := writeConfig(t, defaultsConfig)
	listing, err := List(path)
	if err != nil {
		t.Fatal(err)
	}
	job := listing.Jobs[0]
	if job.Schedule != "" || len(job.Repositories) != 0 || !job.Retention.Empty() || !job.Inherited.Schedule || !job.Inherited.Repositories || !job.Inherited.Retention {
		t.Fatalf("an inheriting job lists no values of its own: %+v", job)
	}
	if listing.Defaults.Schedule != "daily 02:00" || len(listing.InheritedRepositories) != 1 || listing.InheritedRepositories[0] != "only" {
		t.Errorf("listing defaults = %+v %v", listing.Defaults, listing.InheritedRepositories)
	}
	job.Host = "db2"
	if err := Save(path, "inherits", job); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, path)
	for _, key := range []string{"schedule: ", "repositories", "retention"} {
		if strings.Contains(strings.Split(saved, "jobs:")[1], key) {
			t.Errorf("saving an unchanged inheriting job must not write %q:\n%s", key, saved)
		}
	}

	job.Repositories = nil
	if err := Save(path, "", Job{Name: "fresh", Type: config.JobPostgres, Database: "x"}); err != nil {
		t.Errorf("a new job may leave the repositories to the defaults: %v", err)
	}
	if err := Save(writeConfig(t, baseConfig), "", Job{Name: "fresh", Type: config.JobPostgres, Database: "x"}); err == nil {
		t.Error("without defaults a job needs a repository")
	}
}

func TestProblemsListsEveryFieldAtFaultOnce(t *testing.T) {
	job := Job{Name: "a b", Type: config.JobPostgres, Port: 70000, Schedule: "daily 25:00"}
	var fields []string
	for _, problem := range Problems(&job, Known{Repositories: []string{"s3"}}) {
		fields = append(fields, problem.Field)
	}
	if got, want := strings.Join(fields, ","), "name,database,port,schedule,repositories"; got != want {
		t.Errorf("problem fields = %s, want %s", got, want)
	}
}
