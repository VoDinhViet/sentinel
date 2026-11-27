package tui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// settle delivers what cmd produces to the screen, and what that produces in turn, until nothing is left.
func settle(t *testing.T, screen manageModel, cmd tea.Cmd) manageModel {
	t.Helper()
	for range 6 {
		var follow []tea.Cmd
		for _, msg := range messagesOf(cmd) {
			model, next := screen.Update(msg)
			screen = model.(manageModel)
			follow = append(follow, next)
		}
		cmd = tea.Batch(follow...)
	}
	return screen
}

func TestTypingDoesNotWaitForTheSearch(t *testing.T) {
	root := browseTree(t)
	screen, _ := openBrowser(t, filepath.Join(root, "www"))
	model, _ := screen.onKey("u", "u")
	model, cmd := model.(manageModel).onKey("p", "p")
	screen = model.(manageModel)
	if got := rowNames(screen); !slices.Equal(got, []string{".."}) {
		t.Errorf("the deeper matches must not be there before the search answers: %v", got)
	}
	if !screen.browse.searching || cmd == nil {
		t.Error("two letters must start a search in the background")
	}
	if view := plain(screen.render()); !strings.Contains(view, "đang tìm") {
		t.Errorf("the screen does not say the search is under way:\n%s", view)
	}
}

func TestSearchOfAnEarlierFilterIsDropped(t *testing.T) {
	root := browseTree(t)
	screen, _ := openBrowser(t, filepath.Join(root, "www"))
	model, _ := screen.onKey("u", "u")
	model, _ = model.(manageModel).onKey("p", "p")
	screen = model.(manageModel)
	staleDue := deepSearchDueMsg{seq: screen.browse.searchSeq, dir: screen.browse.dir, filter: "up"}

	model, _ = screen.onKey("backspace", "") // the filter is one letter again: no search
	screen = model.(manageModel)
	model, next := screen.Update(staleDue)
	screen = model.(manageModel)
	if next != nil {
		t.Error("a search asked for an older filter must not start")
	}
	model, _ = screen.Update(deepSearchedMsg{seq: staleDue.seq, found: []browseRow{{kind: browseDeeper, name: "x/", path: "/x"}}})
	if got := rowNames(model.(manageModel)); slices.Contains(got, "x/") {
		t.Errorf("an old answer was shown: %v", got)
	}
}

func TestSearchAnswerKeepsTheRowTheUserMovedTo(t *testing.T) {
	root := browseTree(t)
	screen, _ := openBrowser(t, filepath.Join(root, "www"))
	model, _ := screen.onKey("b", "b") // matches the folder beta/
	screen = model.(manageModel)
	model, _ = screen.onKey("e", "e")
	screen = model.(manageModel)
	before := screen.browse.cursor
	model, _ = screen.Update(deepSearchedMsg{seq: screen.browse.searchSeq, found: []browseRow{{kind: browseDeeper, name: "beta/deep/", path: filepath.Join(root, "beta/deep")}}})
	if got := model.(manageModel).browse.cursor; got != before {
		t.Errorf("cursor moved from %d to %d when the answer came", before, got)
	}
}
