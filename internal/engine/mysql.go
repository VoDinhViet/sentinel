package engine

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"sentinel/internal/config"
)

// mysql backs up MySQL and MariaDB with mariadb-dump or mysqldump.
type mysql struct{}

// Seams for tests: where to look for a binary and how to read a client's version.
var (
	lookPath      = exec.LookPath
	clientVersion = func(bin string) string {
		versionOutput, _ := exec.Command(bin, "--version").Output()
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

func (mysql) Type() string    { return config.BackupMySQL }
func (mysql) Tools() []string { return []string{dumpBinary()} }

func (mysql) Database(job *config.Backup) string {
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
func (mysql) Dump(job *config.Backup) (Dump, error) {
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

func (mysql) RestoreHint(job *config.Backup, dir string) string {
	return fmt.Sprintf("mysql -h %s -P %s -u %s -p %s < %s",
		shellQuote(job.MySQL.Host), strconv.Itoa(job.MySQL.Port), shellQuote(job.MySQL.User), shellQuote(job.MySQL.Database),
		shellQuote(filepath.Join(dir, job.MySQL.Database+".sql")))
}
