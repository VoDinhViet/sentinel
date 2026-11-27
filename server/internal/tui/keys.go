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
	switch {
	case m.wide():
		return m.onSplitKey(key)
	case m.opened:
		return m.onServerKey(key)
	}
	return m.onTabKey(key)
}

// onSplitKey is a key in the wide layout: the focus is on the list, or on one panel of the selected server.
func (m model) onSplitKey(key string) (tea.Model, tea.Cmd) {
	if m.inPanels && m.tab == serversTab {
		return m.onPanelKey(key)
	}
	return m.onListKey(key)
}

func (m model) onListKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "esc":
		return m, tea.Quit
	case "r":
		next, ask := m.ask()
		return next, ask
	case "1", "2":
		return m.switchTab(int(key[0] - '1'))
	case "tab", "right", "l", "enter":
		if m.tab == alertsTab {
			return m.switchTab(serversTab)
		}
		m.focusPanel(hostPart)
	case "shift+tab", "left", "h":
		return m.switchTab((m.tab + tabCount - 1) % tabCount)
	default:
		m.cursor = moved(key, m.cursor, len(m.tabTable().lines))
		next, ask := m.syncSelection()
		return next, ask
	}
	return m, nil
}

// switchTab shows a tab of the list, and starts the server on the right over.
func (m model) switchTab(tab int) (tea.Model, tea.Cmd) {
	m.tab, m.cursor, m.inPanels, m.open = tab, 0, false, serverScreen{}
	next, ask := m.syncSelection()
	return next, ask
}

// focusPanel gives a panel of the selected server the focus; past either end it is the list that has it.
func (m *model) focusPanel(part int) {
	m.inPanels = part >= 0 && part < partCount && m.open.name != ""
	if m.inPanels {
		m.open.current, m.open.cursor = part, 0
	}
}

func (m model) onPanelKey(key string) (tea.Model, tea.Cmd) {
	if m.open.confirm != nil {
		return m.onConfirmKey(key)
	}
	switch key {
	case "b", "s":
		return m.onBackupKey(key)
	case "q", "esc", "backspace":
		m.inPanels = false
	case "r":
		next, ask := m.ask()
		return next, ask
	case "tab", "right", "l":
		m.focusPanel(m.open.current + 1)
	case "shift+tab", "left", "h":
		m.focusPanel(m.open.current - 1)
	case "1", "2", "3", "4", "5":
		m.focusPanel(int(key[0] - '1'))
	default:
		m.open.cursor = moved(key, m.open.cursor, len(m.open.table(m.now()).lines))
	}
	return m, nil
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
	if m.open.confirm != nil {
		return m.onConfirmKey(key)
	}
	switch key {
	case "b", "s":
		return m.onBackupKey(key)
	case "q", "esc", "backspace":
		m.opened = false
	case "r":
		next, ask := m.ask()
		return next, ask
	case "tab", "right", "l":
		m.open.current, m.open.cursor = (m.open.current+1)%partCount, 0
	case "shift+tab", "left", "h":
		m.open.current, m.open.cursor = (m.open.current+partCount-1)%partCount, 0
	case "1", "2", "3", "4", "5":
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
