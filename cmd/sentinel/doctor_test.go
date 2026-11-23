package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"sentinel/internal/config"
	"sentinel/internal/store"
)

func TestRunDoctorReportsProblemsWithHints(t *testing.T) {
	cfg := &config.Config{
		StorePath:    filepath.Join(t.TempDir(), "state.db"),
		Repositories: map[string]config.Repository{"local": {URL: filepath.Join(t.TempDir(), "repo"), Password: "x"}},
	}
	findings := runDoctor(context.Background(), cfg, "/etc/sentinel/config.yaml", func(c *config.Config) (*store.Store, error) { return store.Open(c.StorePath) })

	byName := map[string]finding{}
	for _, f := range findings {
		byName[f.name] = f
	}
	if f := byName["config"]; f.err != nil || f.info != "/etc/sentinel/config.yaml" {
		t.Errorf("config = %+v", f)
	}
	if f := byName["store"]; f.err != nil {
		t.Errorf("store = %+v", f)
	}
	notifiers := byName["notifiers"]
	if notifiers.err == nil || !notifiers.warn || !strings.Contains(notifiers.hint, "notifiers:") {
		t.Errorf("notifiers = %+v (a missing channel is a warning with a hint)", notifiers)
	}
	repo := byName["repository local"]
	if repo.err == nil || repo.hint == "" {
		t.Errorf("repository = %+v (a repository that cannot be used says what to check)", repo)
	}
}

func TestRunDoctorStoreFailureHasAHint(t *testing.T) {
	cfg := &config.Config{StorePath: "/proc/nope/state.db"}
	findings := runDoctor(context.Background(), cfg, "c.yaml", func(c *config.Config) (*store.Store, error) { return store.Open(c.StorePath) })
	for _, f := range findings {
		if f.name == "store" {
			if f.err == nil || f.hint == "" {
				t.Errorf("store = %+v, want an error and a hint", f)
			}
			return
		}
	}
	t.Error("no store finding")
}
