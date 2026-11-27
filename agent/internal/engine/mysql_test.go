package engine

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"sentinel/agent/internal/config"
)

func mysqlJob() *config.Job {
	return &config.Job{
		Name: "shop", Type: config.JobMySQL,
		MySQL: &config.MySQL{Host: "db.local", Port: 3307, User: "app", Password: `p"w\d`, Database: "shop"},
	}
}

// fakeTools makes lookPath find only the named binaries and clientVersion
// report version for every client.
func fakeTools(t *testing.T, version string, installed ...string) {
	t.Helper()
	oldLook, oldVersion := lookPath, clientVersion
	t.Cleanup(func() { lookPath, clientVersion = oldLook, oldVersion })
	lookPath = func(bin string) (string, error) {
		if slices.Contains(installed, bin) {
			return "/usr/bin/" + bin, nil
		}
		return "", errors.New("not found")
	}
	clientVersion = func(string) string { return version }
}

func TestMySQLDumpKeepsPasswordOutOfArgv(t *testing.T) {
	fakeTools(t, "mysqldump  Ver 5.7.44", "mysqldump")
	mysqlEngine, _ := For(config.JobMySQL)
	dump, err := mysqlEngine.Dump(mysqlJob())
	if err != nil {
		t.Fatal(err)
	}
	defer dump.Cleanup()

	if strings.Contains(strings.Join(dump.Command, " "), "w\\d") || len(dump.Env) != 0 {
		t.Errorf("password leaked: argv %v env %v", dump.Command, dump.Env)
	}
	if dump.Command[0] != "mysqldump" || !strings.HasPrefix(dump.Command[1], "--defaults-extra-file=") {
		t.Fatalf("--defaults-extra-file must come first: %v", dump.Command)
	}
	for _, want := range []string{"--single-transaction", "--routines", "--triggers", "--events", "--hex-blob"} {
		if !slices.Contains(dump.Command, want) {
			t.Errorf("missing %s: %v", want, dump.Command)
		}
	}
	if slices.Contains(dump.Command, "--column-statistics=0") {
		t.Error("5.7 client must not get --column-statistics")
	}
	if last := dump.Command[len(dump.Command)-2:]; !slices.Equal(last, []string{"--", "shop"}) {
		t.Errorf("database must end the command: %v", dump.Command)
	}
	if dump.Filename != "shop.sql" {
		t.Errorf("filename = %q", dump.Filename)
	}

	path := strings.TrimPrefix(dump.Command[1], "--defaults-extra-file=")
	fileInfo, err := os.Stat(path)
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file: %v, %v", fileInfo, err)
	}
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), `password="p\"w\\d"`) || !strings.Contains(string(content), "port=3307") {
		t.Errorf("option file = %q", content)
	}
	dump.Cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("credentials file not removed by Cleanup")
	}
}

func TestMySQLColumnStatisticsOnlyForMySQL8Client(t *testing.T) {
	mysqlEngine, _ := For(config.JobMySQL)

	fakeTools(t, "mysqldump  Ver 8.0.36 for Linux on x86_64", "mysqldump")
	dump, _ := mysqlEngine.Dump(mysqlJob())
	defer dump.Cleanup()
	if !slices.Contains(dump.Command, "--column-statistics=0") {
		t.Errorf("MySQL 8 client needs --column-statistics=0: %v", dump.Command)
	}

	fakeTools(t, "mariadb-dump from 11.4.3-MariaDB", "mariadb-dump", "mysqldump")
	dump, _ = mysqlEngine.Dump(mysqlJob())
	defer dump.Cleanup()
	if dump.Command[0] != "mariadb-dump" || slices.Contains(dump.Command, "--column-statistics=0") {
		t.Errorf("mariadb-dump is preferred and takes no such flag: %v", dump.Command)
	}
}

func TestMySQLToolsFollowInstalledClient(t *testing.T) {
	mysqlEngine, _ := For(config.JobMySQL)
	fakeTools(t, "", "mariadb-dump")
	if got := mysqlEngine.Tools(); !slices.Equal(got, []string{"mariadb-dump"}) {
		t.Errorf("tools = %v", got)
	}
	fakeTools(t, "")
	if got := mysqlEngine.Tools(); !slices.Equal(got, []string{"mysqldump"}) {
		t.Errorf("with no client doctor should report mysqldump missing, got %v", got)
	}
}

func TestMySQLDumpRequiresSettings(t *testing.T) {
	mysqlEngine, _ := For(config.JobMySQL)
	if _, err := mysqlEngine.Dump(&config.Job{Type: config.JobMySQL}); err == nil {
		t.Fatal("want error when mysql settings are missing")
	}
}

func TestMySQLTLSOption(t *testing.T) {
	fakeTools(t, "", "mariadb-dump")
	mysqlEngine, _ := For(config.JobMySQL)
	for tls, want := range map[string]string{"disabled": "loose-skip-ssl", "required": "loose-ssl\n", "": ""} {
		job := mysqlJob()
		job.MySQL.TLS = tls
		dump, err := mysqlEngine.Dump(job)
		if err != nil {
			t.Fatal(err)
		}
		content, _ := os.ReadFile(strings.TrimPrefix(dump.Command[1], "--defaults-extra-file="))
		dump.Cleanup()
		if has := strings.Contains(string(content), "ssl"); has != (want != "") || !strings.Contains(string(content), want) {
			t.Errorf("tls %q: option file = %q", tls, content)
		}
	}
}
