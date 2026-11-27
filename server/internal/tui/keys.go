package tui

import (
	tea "charm.land/bubbletea/v2"
)

func (m model) onKey(key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.help { // any key closes the help
		m.help = false
		return m, nil
	}
	if key == "?" {
		m.help = true
		return m, nil
	}
	if m.opened {
		return m.onServerKey(key)
	}
	return m.onTabKey(key)
}

func (m model) onTabKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "esc":
		return m, tea.Quit
	case "r":
		next, ask := m.ask()
		return next, ask
	case "tab", "right", "l":
		m.tab, m.cursor = (m.tab+1)%tabCount, 0
	case "shift+tab", "left", "h":
		m.tab, m.cursor = (m.tab+tabCount-1)%tabCount, 0
	case "1", "2":
		m.tab, m.cursor = int(key[0]-'1'), 0
	case "enter":
		return m.openServer()
	default:
		m.cursor = moved(key, m.cursor, m.currentLines())
	}
	return m, nil
}

func (m model) onServerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "esc", "backspace":
		m.opened = false
	case "r":
		next, ask := m.ask()
		return next, ask
	case "tab", "right", "l":
		m.open.current, m.open.cursor = (m.open.current+1)%partCount, 0
	case "shift+tab", "left", "h":
		m.open.current, m.open.cursor = (m.open.current+partCount-1)%partCount, 0
	case "1", "2", "3", "4":
		m.open.current, m.open.cursor = int(key[0]-'1'), 0
	default:
		m.open.cursor = moved(key, m.open.cursor, m.currentLines())
	}
	return m, nil
}

// moved is the cursor after a key that moves it, inside a list of count lines.
func moved(key string, cursor, count int) int {
	last := max(count-1, 0)
	switch key {
	case "down", "j":
		return min(cursor+1, last)
	case "up", "k":
		return max(cursor-1, 0)
	case "pgdown":
		return min(cursor+10, last)
	case "pgup":
		return max(cursor-10, 0)
	case "g", "home":
		return 0
	case "G", "end":
		return last
	}
	return cursor
}

// openServer opens the screen of the server under the cursor, and asks for its report.
func (m model) openServer() (tea.Model, tea.Cmd) {
	if m.tab != serversTab || m.cursor >= len(m.servers) {
		return m, nil
	}
	name := m.servers[m.cursor].Name
	m.opened = true
	m.open = serverScreen{name: name, loading: true}
	for _, known := range m.servers { // the line of the list is shown until the report arrives
		if known.Name == name {
			m.open.detail.Agent = known
		}
	}
	return m, m.askDetail(name)
}
