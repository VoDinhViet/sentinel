package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/configedit"
)

func TestPasteDropsTheConnectionResultOfOtherValues(t *testing.T) {
	screen, _ := formModel(t)
	screen = openAddForm(t, screen)
	screen = keys(t, screen, "right") // postgres is the first database type
	screen = tabTo(t, screen, func(field formField) bool { return field.key == "host" })
	screen.form.connect = connectState{text: "kết nối được (42ms)"}

	model, _ := screen.Update(tea.PasteMsg{Content: "db.internal"})
	screen = model.(manageModel)
	if screen.form.field("host").value != "db.internal" {
		t.Fatalf("host = %q", screen.form.field("host").value)
	}
	if screen.form.connect.text != "" {
		t.Errorf("the result of the test of other values is still shown: %q", screen.form.connect.text)
	}
}

func TestSaveProblemOfAFieldNotOnScreenShowsAsTheProblemOfTheForm(t *testing.T) {
	screen, configFile := formModel(t)
	configFile.saveErr = &configedit.InvalidError{Field: "store_at", Message: "thư mục không hợp lệ"}
	screen = openAddForm(t, screen)
	screen.form.field("name").value, screen.form.field("database").value = "x", "x"
	focus := screen.form.focus
	if screen.form.field("storage").value != storageAuto {
		t.Fatalf("storage = %q: store_at must be hidden for this test", screen.form.field("storage").value)
	}

	screen = keys(t, screen, "ctrl+s")
	if !slicesContains(screen.form.visible(), screen.form.focus) {
		t.Errorf("the focus moved to field %d, which is not on screen", screen.form.focus)
	}
	if screen.form.focus != focus {
		t.Errorf("the focus moved from %d to %d", focus, screen.form.focus)
	}
	if view := plain(screen.render()); !strings.Contains(view, "thư mục không hợp lệ") {
		t.Errorf("the problem is nowhere on the screen:\n%s", view)
	}
}

func slicesContains(shown []int, index int) bool {
	for _, candidate := range shown {
		if candidate == index {
			return true
		}
	}
	return false
}

func TestSecondSaveAfterTheRepositoryWasCreatedAndTheJobFailed(t *testing.T) {
	screen, configFile := formModel(t)
	screen = openAddForm(t, screen)
	screen.form.field("name").value, screen.form.field("database").value = "shop", "shop"
	screen.form.field("storage").value, screen.form.field("store_at").value = storageFolder, t.TempDir()+"/dumps"

	var created []configedit.Repository
	saveJobErr := errors.New("disk full")
	screen.actions.saveRepository = func(_ string, repository configedit.Repository) error {
		for _, existing := range created {
			if existing.Name == repository.Name {
				return configedit.ErrExists
			}
		}
		created = append(created, repository)
		return nil
	}
	screen.actions.overview = func() (configedit.Overview, error) {
		return configedit.Overview{Repositories: created}, nil
	}
	screen.actions.saveJob = func(string, configedit.Job) error { return saveJobErr }

	screen = keys(t, screen, "ctrl+s")
	if len(created) != 1 || !screen.noticeBad || !strings.Contains(screen.notice, "disk full") {
		t.Fatalf("first try: created %d repositories, notice %q", len(created), screen.notice)
	}

	// The job fails for a reason of its own; after it is fixed the same form saves again.
	screen = openAddForm(t, screen)
	screen.form.field("name").value, screen.form.field("database").value = "shop", "shop"
	screen.form.field("storage").value, screen.form.field("store_at").value = storageFolder, created[0].URL
	saveJobErr = nil
	var saved []configedit.Job
	screen.actions.saveJob = func(_ string, job configedit.Job) error { saved = append(saved, job); return nil }
	screen = keys(t, screen, "ctrl+s")
	if len(saved) != 1 || !screen.onTabs() || screen.noticeBad {
		t.Fatalf("second try: saved %d jobs, screens %d, notice %q", len(saved), len(screen.screens), screen.notice)
	}
	if len(configFile.saved) != 0 {
		t.Error("the fake config must not have been used")
	}
}

func TestTabCandidatesAreDroppedWhenTheFieldIsLeft(t *testing.T) {
	screen, _ := formModel(t)
	screen = openAddForm(t, screen)
	screen.form.field("type").value = config.JobFiles // the type that has a Paths field
	screen = tabTo(t, screen, func(field formField) bool { return field.kind == fieldPaths })
	index := screen.form.focus
	screen.form.fields[index].options = []string{"/etc/", "/etc2/"}
	screen.form.focusOn(screen.form.visible()[0]) // what a Tab that completes nothing does: it moves on, keeping the list
	screen.form.focusOn(index)                    // and shift+tab comes back
	if got := screen.form.fields[index].options; len(got) != 0 {
		t.Errorf("the candidates of an earlier Tab came back: %v", got)
	}
}
