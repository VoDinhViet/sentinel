package configedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/agent/internal/config"
)

func envOf(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}

func TestConnectionJobFillsDefaultsAndReadsThePasswordFromItsVariable(t *testing.T) {
	job, err := ConnectionJob(Job{Type: config.JobPostgres, Database: "shop", PasswordEnv: "PG_PW"}, "", envOf(map[string]string{"PG_PW": "pw"}))
	if err != nil {
		t.Fatal(err)
	}
	if pg := job.Postgres; pg == nil || pg.Host != "localhost" || pg.Port != 5432 || pg.User != "postgres" || pg.Password != "pw" || pg.Database != "shop" {
		t.Errorf("postgres = %+v", job.Postgres)
	}
	job, err = ConnectionJob(Job{Type: config.JobMongoDB, Database: "crm", User: "root", AuthDatabase: ""}, "", envOf(nil))
	if err != nil || job.MongoDB.Port != 27017 || job.MongoDB.AuthDatabase != "admin" || job.MongoDB.Password != "" {
		t.Errorf("mongodb = %+v, %v", job.MongoDB, err)
	}
	job, err = ConnectionJob(Job{Type: config.JobMySQL, Database: "shop", TLS: "disabled"}, "", envOf(nil))
	if err != nil || job.MySQL.User != "root" || job.MySQL.Port != 3306 || job.MySQL.TLS != "disabled" {
		t.Errorf("mysql = %+v, %v", job.MySQL, err)
	}
}

func TestConnectionJobRefusesWhatCannotBeTested(t *testing.T) {
	tests := []struct {
		name string
		job  Job
		want string
	}{
		{"unset variable", Job{Type: config.JobPostgres, Database: "shop", PasswordEnv: "MISSING_PW"}, "MISSING_PW chưa được đặt"},
		{"bad port", Job{Type: config.JobPostgres, Database: "shop", Port: 70000}, "port"},
		{"no database", Job{Type: config.JobPostgres}, "database"},
		{"files job", Job{Type: config.JobFiles, Paths: []string{"/etc"}}, "database"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ConnectionJob(test.job, "", envOf(nil)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestConnectionJobTakesTheSavedLiteralPassword(t *testing.T) {
	job, err := ConnectionJob(Job{Type: config.JobMySQL, Database: "shop", PasswordLiteral: true}, "literal-pw", envOf(nil))
	if err != nil || job.MySQL.Password != "literal-pw" {
		t.Errorf("mysql = %+v, %v", job.MySQL, err)
	}
	// A variable named in the form wins over the literal that the file still holds.
	job, err = ConnectionJob(Job{Type: config.JobMySQL, Database: "shop", PasswordLiteral: true, PasswordEnv: "NEW_PW"}, "literal-pw", envOf(map[string]string{"NEW_PW": "new"}))
	if err != nil || job.MySQL.Password != "new" {
		t.Errorf("mysql = %+v, %v", job.MySQL, err)
	}
}

func TestSavedPasswordReadsTheLiteralWithoutExpandingVariables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "repositories:\n  local: {url: /r, password: \"${RESTIC_PASSWORD}\"}\njobs:\n" +
		"  - name: lit\n    type: postgres\n    postgres: {database: a, password: plain-pw}\n    repositories: [local]\n" +
		"  - name: var\n    type: postgres\n    postgres: {database: b, password: \"${B_PW}\"}\n    repositories: [local]\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SavedPassword(path, "lit"); got != "plain-pw" {
		t.Errorf("literal = %q", got)
	}
	if got := SavedPassword(path, "nope"); got != "" {
		t.Errorf("unknown job = %q", got)
	}
}
