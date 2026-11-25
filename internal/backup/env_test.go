package backup

import (
	"slices"
	"testing"

	"sentinel/internal/config"
)

func TestResticClientDoesNotInheritTheVariablesTheConfigRefersTo(t *testing.T) {
	runner := &Runner{Config: &config.Config{
		Repositories: map[string]config.Repository{"local": {URL: "/var/backups/repo", Password: "pw"}},
		EnvRefs:      []string{"RESTIC_PW", "TG_TOKEN"},
	}}
	client, err := runner.Restic("local")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"RESTIC_PW", "TG_TOKEN"}; !slices.Equal(client.DropEnv, want) {
		t.Errorf("DropEnv = %v, want %v", client.DropEnv, want)
	}
}
