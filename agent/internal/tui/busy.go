package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

// busyScreen shows an action that takes a while (listing snapshots, a restore, a test message) with
// a spinner. Only ctrl+c works on it: it cancels the action, whose answer then closes the screen.
type busyScreen struct {
	text   string
	start  time.Time
	frame  int
	cancel context.CancelFunc
}

func (s busyScreen) onKey(m manageModel, key, _ string) (manageModel, tea.Cmd) {
	if key == "ctrl+c" && s.cancel != nil {
		s.cancel()
		s.text += " (đang huỷ: restic có thể mất tới 30 giây để dừng gọn)"
		return m.replaceTop(s), nil
	}
	return m, nil
}

func (s busyScreen) render(m manageModel) []string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	elapsed := time.Since(s.start).Round(time.Second)
	return []string{
		lipglossAccent(frames[s.frame%len(frames)]) + " " + s.text + dimStyle.Render("  "+elapsed.String()),
		"",
		m.keyBar(2, "ctrl+c", "huỷ"),
	}
}

// spin turns the spinner a frame later.
func (m manageModel) spin() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

// onSpin advances the spinner of the busy screen; once it is gone the ticks stop.
func (m manageModel) onSpin() (manageModel, tea.Cmd) {
	busy, ok := m.top().(busyScreen)
	if !ok {
		return m, nil
	}
	busy.frame++
	return m.replaceTop(busy), m.spin()
}

// startBusy shows the busy screen and returns the context the action runs with; ctrl+c on the
// screen cancels it.
func (m *manageModel) startBusy(text string) context.Context {
	ctx, cancel := context.WithCancel(m.ctx)
	*m = m.push(busyScreen{text: text, start: time.Now(), cancel: cancel})
	m.notice = ""
	return ctx
}

// endBusy closes the busy screen when the action has answered.
func (m *manageModel) endBusy() {
	if busy, ok := m.top().(busyScreen); ok {
		busy.cancel()
		*m = m.pop()
	}
}
