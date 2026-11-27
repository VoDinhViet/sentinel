package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"sentinel/agent/internal/config"
)

// mysql backs up MySQL and MariaDB with mariadb-dump or mysqldump.
type mysql struct{}

// clientVersionTimeout bounds `<client> --version`, which runs before every dump and probe.
var clientVersionTimeout = 10 * time.Second

// Seams for tests: where to look for a binary and how to read a client's version.
var (
	lookPath      = exec.LookPath
	clientVersion = func(bin string) string {
		ctx, cancel := context.WithTimeout(context.Background(), clientVersionTimeout)
		defer cancel()
		versionOutput, _ := exec.CommandContext(ctx, bin, "--version").Output()
		return string(versionOutput)
	}
)

// dumpBinary prefers mariadb-dump, falling back to mysqldump. When neither is
// installed it names mysqldump, so `sentinel doctor` reports that one missing.
func dumpBinary() string {
	for _, bin := range []string{"mariadb-dump", "mysqldump"} {
		if _, err := lookPath(bin); err == nil {
			return bin
		}
	}
	return "mysqldump"
}

func (mysql) Type() string    { return config.JobMySQL }
func (mysql) Tools() []string { return []string{dumpBinary()} }

func (mysql) Database(job *config.Job) string {
	if job.MySQL == nil {
		return ""
	}
	return job.MySQL.Database
}

var mysql8Client = regexp.MustCompile(`Ver 8\.`)

// optionFileValue quotes a value for a MySQL option file.
func optionFileValue(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value) + `"`
}

// Dump writes a consistent SQL dump (single transaction, so InnoDB tables need
// no lock) including routines, triggers and events. Credentials go in a
// temporary option file, never on argv.
func (mysql) Dump(job *config.Job) (Dump, error) {
	settings := job.MySQL
	if settings == nil {
		return Dump{}, errors.New("mysql settings are missing")
	}
	optionFile := fmt.Sprintf("[client]\nhost=%s\nport=%d\nuser=%s\n", optionFileValue(settings.Host), settings.Port, optionFileValue(settings.User))
	if settings.Password != "" {
		optionFile += "password=" + optionFileValue(settings.Password) + "\n"
	}
	// MariaDB and MySQL clients spell this differently; "loose-" makes each
	// ignore the option it doesn't know.
	switch settings.TLS {
	case "disabled":
		optionFile += "loose-skip-ssl\nloose-ssl-mode=DISABLED\n"
	case "required":
		optionFile += "loose-ssl\nloose-ssl-mode=REQUIRED\n"
	}
	path, cleanup, err := writeSecretFile("sentinel-mysql-*.cnf", optionFile)
	if err != nil {
		return Dump{}, fmt.Errorf("write mysql credentials file: %w", err)
	}

	bin := dumpBinary()
	// --defaults-extra-file must be the first option.
	command := []string{bin, "--defaults-extra-file=" + path,
		"--single-transaction", "--routines", "--triggers", "--events", "--hex-blob"}
	// A MySQL 8 client asks for column statistics that a 5.7 server (and
	// MariaDB) doesn't have; MariaDB's own client doesn't know the flag.
	if bin == "mysqldump" && mysql8Client.MatchString(clientVersion(bin)) {
		command = append(command, "--column-statistics=0")
	}
	command = append(command, "--", settings.Database)
	return Dump{Command: command, Filename: settings.Database + ".sql", Cleanup: cleanup}, nil
}

func (mysql) RestoreHint(job *config.Job, dir string) string {
	return fmt.Sprintf("mysql -h %s -P %s -u %s -p %s < %s",
		shellQuote(job.MySQL.Host), strconv.Itoa(job.MySQL.Port), shellQuote(job.MySQL.User), shellQuote(job.MySQL.Database),
		shellQuote(filepath.Join(dir, job.MySQL.Database+".sql")))
}

// LightDump turns the dump into one that writes no table, routine, trigger or event, so a probe
// can run it to the end: it still logs in and selects the database, which is what can fail.
func (mysql) LightDump(dump Dump) Dump {
	separator := slices.Index(dump.Command, "--")
	if separator < 0 {
		return dump
	}
	light := slices.Clone(dump.Command)
	extra := []string{"--no-data", "--no-create-info", "--skip-routines", "--skip-triggers", "--skip-events"}
	dump.Command = slices.Concat(light[:separator], extra, light[separator:])
	return dump
}
