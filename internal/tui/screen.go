package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
)

// screen is a full-window view opened over the tabs: it takes the keys and draws itself. The model
// keeps them in a stack. Opening a screen pushes it, esc or a finished action pops it, and with the
// stack empty the tabs show. A screen keeps its own state in its fields.
type screen interface {
	// onKey handles one key. m is the whole model; its stack ends with this screen.
	onKey(m manageModel, key, text string) (manageModel, tea.Cmd)
	// render is the lines of the screen; the last one is its key bar.
	render(m manageModel) []string
}

// top is the screen on show, nil when the tabs are.
func (m manageModel) top() screen {
	if len(m.screens) == 0 {
		return nil
	}
	return m.screens[len(m.screens)-1]
}

func (m manageModel) push(next screen) manageModel {
	m.screens = append(slices.Clone(m.screens), next)
	return m
}

// pop closes the screen on show, which shows the one under it or the tabs.
func (m manageModel) pop() manageModel {
	if len(m.screens) > 0 {
		m.screens = slices.Clone(m.screens[:len(m.screens)-1])
	}
	return m
}

// replaceTop swaps the screen on show for next, to save what the screen changed in itself.
func (m manageModel) replaceTop(next screen) manageModel {
	m.screens = append(slices.Clone(m.screens[:len(m.screens)-1]), next)
	return m
}
