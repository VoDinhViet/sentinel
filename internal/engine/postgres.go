package engine

import (
	"errors"
	"fmt"
	"strconv"

	"sentinel/internal/config"
)

type postgres struct{}

func (postgres) Type() string    { return config.BackupPostgres }
func (postgres) Tools() []string { return []string{"pg_dump"} }

func (postgres) Database(job *config.Backup) string {
	if job.Postgres == nil {
		return ""
	}
	return job.Postgres.Database
}

// Dump runs pg_dump in custom format (compressed, restorable with pg_restore).
func (postgres) Dump(job *config.Backup) (Dump, error) {
	pg := job.Postgres
	if pg == nil {
		return Dump{}, errors.New("postgres settings are missing")
	}
	dump := Dump{
		Command: []string{
			"pg_dump", "--format=custom", "--no-password",
			"--host", pg.Host, "--port", strconv.Itoa(pg.Port),
			"--username", pg.User, "--dbname", pg.Database,
		},
		Filename: pg.Database + ".dump",
	}
	if pg.Password != "" {
		dump.Env = []string{"PGPASSWORD=" + pg.Password}
	}
	return dump, nil
}

func (postgres) RestoreHint(job *config.Backup, dir string) string {
	return fmt.Sprintf("pg_restore --clean --if-exists -d %s %s/%s.dump", job.Postgres.Database, dir, job.Postgres.Database)
}
