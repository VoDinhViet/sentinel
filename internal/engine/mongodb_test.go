package engine

import (
	"os"
	"slices"
	"strings"
	"testing"

	"sentinel/internal/config"
)

func mongoJob() *config.Job {
	return &config.Job{
		Name: "crm", Type: config.JobMongoDB,
		MongoDB: &config.MongoDB{Host: "mongo.local", Port: 27018, User: "app", Password: `s"3cret`, Database: "crm", AuthDatabase: "admin"},
	}
}

func TestMongoDumpKeepsPasswordOutOfArgv(t *testing.T) {
	mongoEngine, _ := For(config.JobMongoDB)
	dump, err := mongoEngine.Dump(mongoJob())
	if err != nil {
		t.Fatal(err)
	}
	defer dump.Cleanup()

	joined := strings.Join(dump.Command, " ")
	if strings.Contains(joined, "3cret") || len(dump.Env) != 0 {
		t.Errorf("password leaked: argv %v env %v", dump.Command, dump.Env)
	}
	for _, want := range []string{"mongodump", "--archive", "--gzip", "--host mongo.local", "--port 27018", "--db crm", "--username app", "--authenticationDatabase admin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q: %s", want, joined)
		}
	}
	if dump.Filename != "crm.archive.gz" {
		t.Errorf("filename = %q", dump.Filename)
	}

	path := strings.TrimPrefix(dump.Command[len(dump.Command)-1], "--config=")
	fileInfo, err := os.Stat(path)
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("config file: %v, %v", fileInfo, err)
	}
	if content, _ := os.ReadFile(path); string(content) != "password: \"s\\\"3cret\"\n" {
		t.Errorf("config file = %q", content)
	}
	dump.Cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("config file not removed by Cleanup")
	}
}

func TestMongoDumpWithoutAuth(t *testing.T) {
	job := mongoJob()
	job.MongoDB.User, job.MongoDB.Password, job.MongoDB.AuthDatabase = "", "", ""
	mongoEngine, _ := For(config.JobMongoDB)
	dump, err := mongoEngine.Dump(job)
	if err != nil {
		t.Fatal(err)
	}
	if dump.Cleanup != nil {
		t.Error("no credentials, so no temporary file to clean up")
	}
	for _, arg := range dump.Command {
		if arg == "--username" || strings.HasPrefix(arg, "--config") {
			t.Errorf("unexpected auth argument in %v", dump.Command)
		}
	}
	if !slices.Contains(dump.Command, "--archive") {
		t.Errorf("must stream an archive to stdout: %v", dump.Command)
	}
}

func TestMongoRestoreHintAndTools(t *testing.T) {
	mongoEngine, _ := For(config.JobMongoDB)
	if got := mongoEngine.Tools(); !slices.Equal(got, []string{"mongodump"}) {
		t.Errorf("tools = %v", got)
	}
	want := "mongorestore --host mongo.local --port 27018 --drop --gzip --archive=/r/crm.archive.gz --nsInclude 'crm.*'"
	if got := mongoEngine.RestoreHint(mongoJob(), "/r"); got != want {
		t.Errorf("hint = %q", got)
	}
}
