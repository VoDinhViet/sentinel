package configedit

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"sentinel/agent/internal/config"
)

// connectionFields are the fields of a job that decide whether its database can be reached.
var connectionFields = []string{"type", "database", "user", "host", "port", "password_env", "tls"}

// ConnectionJob builds the config job a connection test uses from a job as the editor holds
// it: the defaults a loaded config would have, and the password read from its environment
// variable. lookupEnv is os.LookupEnv; savedPassword is the literal password the file holds,
// used when the job keeps one (PasswordLiteral) and names no variable. A variable that is not
// set is an error, so no test runs with an empty password by mistake.
func ConnectionJob(job Job, savedPassword string, lookupEnv func(string) (string, bool)) (*config.Job, error) {
	if !slices.Contains(DatabaseTypes, job.Type) {
		return nil, errors.New("chỉ job database mới có kết nối để thử")
	}
	for _, problem := range Problems(&job, Known{InheritsRepositories: true}) {
		if slices.Contains(connectionFields, problem.Field) {
			return nil, problem
		}
	}
	password := ""
	switch {
	case job.PasswordEnv != "":
		value, ok := lookupEnv(job.PasswordEnv)
		if !ok {
			return nil, fmt.Errorf("biến môi trường %s chưa được đặt", job.PasswordEnv)
		}
		password = value
	case job.PasswordLiteral:
		password = savedPassword
	}
	built := &config.Job{Name: job.Name, Type: job.Type}
	switch job.Type {
	case config.JobPostgres:
		built.Postgres = &config.Postgres{Host: job.Host, Port: job.Port, User: job.User, Password: password, Database: job.Database}
	case config.JobMySQL:
		built.MySQL = &config.MySQL{Host: job.Host, Port: job.Port, User: job.User, Password: password, Database: job.Database, TLS: job.TLS}
	case config.JobMongoDB:
		built.MongoDB = &config.MongoDB{Host: job.Host, Port: job.Port, User: job.User, Password: password, Database: job.Database, AuthDatabase: job.AuthDatabase}
	}
	config.ApplyDatabaseDefaults(built)
	return built, nil
}

// SavedPassword is the literal password the config file holds for a job, "" when it has none
// or the file cannot be read. It is only used to test a connection and never shown or logged.
func SavedPassword(path, jobName string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	cfg, err := config.ParseWith(raw, config.ParseOptions{})
	if err != nil {
		return ""
	}
	job, err := cfg.FindJob(jobName)
	if err != nil {
		return ""
	}
	switch {
	case job.Postgres != nil:
		return job.Postgres.Password
	case job.MySQL != nil:
		return job.MySQL.Password
	case job.MongoDB != nil:
		return job.MongoDB.Password
	}
	return ""
}
