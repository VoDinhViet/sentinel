// Package tui is the terminal screen of sentinelctl: the servers and their alerts, and one server in
// detail. It only reads, through the same client the commands use, and draws what the control plane said.
package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/server/internal/client"
	"sentinel/server/internal/ctlapi"
)

const (
	refreshEvery = 5 * time.Second
	askTimeout   = 15 * time.Second // a call that hangs must still end, or the screen would never refresh
)

// Source is the control plane as the screen sees it; *client.Client is one.
type Source interface {
	Agents(ctx context.Context) ([]ctlapi.Agent, error)
	Agent(ctx context.Context, name string) (ctlapi.AgentDetail, error)
	Alerts(ctx context.Context) ([]ctlapi.Alert, error)
}

// Run shows the screen until the person leaves it. It returns an error when the session ended, so the
// command can tell them to sign in again.
func Run(ctx context.Context, source Source) error {
	final, err := tea.NewProgram(newModel(ctx, source, time.Now), tea.WithContext(ctx)).Run()
	if err != nil {
		return err
	}
	return final.(model).fatal
}

// The tabs, in the order of the keys 1 and 2.
const (
	serversTab = iota
	alertsTab
	tabCount
)

// model is the whole screen. The tabs show unless open is set: then it is the screen of one server.
type model struct {
	ctx    context.Context
	source Source
	now    func() time.Time

	width, height int
	tab           int
	cursor        int
	help          bool
	fatal         error // the session ended: the program quits and Run returns it

	servers  []ctlapi.Agent
	alerts   []ctlapi.Alert
	loaded   bool
	loading  bool
	loadErr  error
	loadedAt time.Time

	opened bool
	open   serverScreen
}

func newModel(ctx context.Context, source Source, now func() time.Time) model {
	// The first ask starts with the program (Init), so the model begins as asking.
	return model{ctx: ctx, source: source, now: now, width: 100, height: 30, loading: true}
}

// Messages.
type (
	listMsg struct {
		servers []ctlapi.Agent
		alerts  []ctlapi.Alert
		err     error
		at      time.Time
	}
	detailMsg struct {
		name   string
		detail ctlapi.AgentDetail
		err    error
	}
	refreshMsg struct{}
)

func (m model) Init() tea.Cmd {
	return tea.Batch(m.askList(), tick(), tea.RequestBackgroundColor)
}

func tick() tea.Cmd { return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return refreshMsg{} }) }

// askList reads the servers and the alerts once.
func (m model) askList() tea.Cmd {
	source, ctx, now := m.source, m.ctx, m.now
	return func() tea.Msg {
		askCtx, cancel := context.WithTimeout(ctx, askTimeout)
		defer cancel()
		servers, err := source.Agents(askCtx)
		if err != nil {
			return listMsg{err: err, at: now()}
		}
		alerts, err := source.Alerts(askCtx)
		return listMsg{servers: servers, alerts: alerts, err: err, at: now()}
	}
}

// askDetail reads one server with its latest report.
func (m model) askDetail(name string) tea.Cmd {
	source, ctx := m.source, m.ctx
	return func() tea.Msg {
		askCtx, cancel := context.WithTimeout(ctx, askTimeout)
		defer cancel()
		detail, err := source.Agent(askCtx, name)
		return detailMsg{name: name, detail: detail, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		applyPalette(msg.IsDark()) // the terminal answered: use the palette that suits its background
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case listMsg:
		return m.afterList(msg)
	case detailMsg:
		return m.afterDetail(msg)
	case refreshMsg:
		return m.refresh()
	case tea.KeyPressMsg:
		return m.onKey(msg.String())
	}
	return m, nil
}

// fail ends the program when the session is over: nothing the screen asks would work again.
func (m model) fail(err error) (model, tea.Cmd, bool) {
	if errors.Is(err, client.ErrSessionEnded) || errors.Is(err, client.ErrNoSession) {
		m.fatal = err
		return m, tea.Quit, true
	}
	return m, nil, false
}

func (m model) afterList(msg listMsg) (tea.Model, tea.Cmd) {
	m.loading, m.loadErr = false, msg.err
	if next, quit, over := m.fail(msg.err); over {
		return next, quit
	}
	if msg.err == nil {
		m.servers, m.alerts, m.loaded, m.loadedAt = msg.servers, msg.alerts, true, msg.at
	}
	m.cursor = min(m.cursor, max(len(m.tabTable().lines)-1, 0)) // the list, even while a server is open
	return m, nil
}

func (m model) afterDetail(msg detailMsg) (tea.Model, tea.Cmd) {
	if !m.opened || m.open.name != msg.name { // the person went back, or to another server
		return m, nil
	}
	m.open.loading, m.open.err = false, msg.err
	if next, quit, over := m.fail(msg.err); over {
		return next, quit
	}
	if msg.err == nil {
		m.open.detail, m.open.read = msg.detail, true
	}
	m.open.cursor = min(m.open.cursor, max(len(m.open.table(m.now()).lines)-1, 0))
	return m, nil
}

// refresh is the timer: ask again, and set the next one.
func (m model) refresh() (tea.Model, tea.Cmd) {
	next, ask := m.ask()
	return next, tea.Batch(tick(), ask)
}

// ask reads the list, and the open server, unless the last ask has not answered yet: a slow control
// plane never piles asks up.
func (m model) ask() (model, tea.Cmd) {
	var cmds []tea.Cmd
	if !m.loading {
		m.loading = true
		cmds = append(cmds, m.askList())
	}
	if m.opened && !m.open.loading {
		m.open.loading = true
		cmds = append(cmds, m.askDetail(m.open.name))
	}
	return m, tea.Batch(cmds...)
}

// currentLines is how many lines the table on show has, to keep the cursor inside it.
func (m model) currentLines() int {
	if m.opened {
		return len(m.open.table(m.now()).lines)
	}
	return len(m.tabTable().lines)
}
