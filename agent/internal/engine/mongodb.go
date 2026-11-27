package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"sentinel/agent/internal/config"
)

type mongodb struct{}

func (mongodb) Type() string    { return config.JobMongoDB }
func (mongodb) Tools() []string { return []string{"mongodump"} }

func (mongodb) Database(job *config.Job) string {
	if job.MongoDB == nil {
		return ""
	}
	return job.MongoDB.Database
}

// Dump streams a gzip-compressed mongodump archive of one database. The
// password goes in a temporary --config file, never on argv.
func (mongodb) Dump(job *config.Job) (Dump, error) {
	mongo := job.MongoDB
	if mongo == nil {
		return Dump{}, errors.New("mongodb settings are missing")
	}
	command := []string{"mongodump", "--archive", "--gzip",
		"--host", mongo.Host, "--port", strconv.Itoa(mongo.Port), "--db", mongo.Database}
	dump := Dump{Filename: mongo.Database + ".archive.gz"}
	if mongo.User != "" {
		command = append(command, "--username", mongo.User, "--authenticationDatabase", mongo.AuthDatabase)
	}
	if mongo.Password != "" {
		// A JSON string is a valid YAML double-quoted scalar.
		quoted, _ := json.Marshal(mongo.Password)
		path, cleanup, err := writeSecretFile("sentinel-mongo-*.yaml", "password: "+string(quoted)+"\n")
		if err != nil {
			return Dump{}, fmt.Errorf("write mongodb credentials file: %w", err)
		}
		command = append(command, "--config="+path)
		dump.Cleanup = cleanup
	}
	dump.Command = command
	return dump, nil
}

func (mongodb) RestoreHint(job *config.Job, dir string) string {
	return fmt.Sprintf("mongorestore --host %s --port %d --drop --gzip --archive=%s --nsInclude %s",
		shellQuote(job.MongoDB.Host), job.MongoDB.Port,
		shellQuote(filepath.Join(dir, job.MongoDB.Database+".archive.gz")), shellQuote(job.MongoDB.Database+".*"))
}
