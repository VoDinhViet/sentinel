package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/config"
	"sentinel/internal/store"
)

func TestRunReportsProblemsWithHints(t *testing.T) {
	cfg := &config.Config{
		StorePath:    filepath.Join(t.TempDir(), "state.db"),
		Repositories: map[string]config.Repository{"local": {URL: filepath.Join(t.TempDir(), "repo"), Password: "x"}},
	}
	findings := Run(context.Background(), cfg, "/etc/sentinel/config.yaml", func(c *config.Config) (*store.Store, error) { return store.Open(c.StorePath) })

	byName := map[string]Finding{}
	for _, f := range findings {
		byName[f.Name] = f
	}
	if f := byName["config"]; f.Err != nil || f.Info != "/etc/sentinel/config.yaml" {
		t.Errorf("config = %+v", f)
	}
	if f := byName["store"]; f.Err != nil {
		t.Errorf("store = %+v", f)
	}
	notifiers := byName["notifiers"]
	if notifiers.Err == nil || !notifiers.Warn || !strings.Contains(notifiers.Hint, "notifiers:") {
		t.Errorf("notifiers = %+v (a missing notifier is a warning with a hint)", notifiers)
	}
	repo := byName["repository local"]
	if repo.Err == nil || repo.Hint == "" {
		t.Errorf("repository = %+v (a repository that cannot be used says what to check)", repo)
	}
}

func TestRunStoreFailureHasAHint(t *testing.T) {
	cfg := &config.Config{StorePath: "/proc/nope/state.db"}
	findings := Run(context.Background(), cfg, "c.yaml", func(c *config.Config) (*store.Store, error) { return store.Open(c.StorePath) })
	for _, f := range findings {
		if f.Name == "store" {
			if f.Err == nil || f.Hint == "" {
				t.Errorf("store = %+v, want an error and a hint", f)
			}
			return
		}
	}
	t.Error("no store finding")
}
