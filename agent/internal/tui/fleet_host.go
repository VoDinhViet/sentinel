package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/fleet"
	"sentinel/agent/internal/host"

	"sentinel/pkg/agentapi"
)

// fleetHostReadMsg carries the reading of one server's machine started by startFleetHost.
type fleetHostReadMsg struct {
	server string
	host   agentapi.HostStatus
	err    error
}

// fleetHostState is the machine of the server whose screen is open: read when the screen opens and
// again with every round, never for the servers only listed.
type fleetHostState struct {
	server  string // whose machine this is; a reading of another server is dropped
	reading bool
	status  agentapi.HostStatus
	read    bool // status holds a reading
	err     error
}

// startFleetHost reads the machine of the named server in the background, unless a reading is
// already under way. It returns the model unchanged when the server is not in the last round.
func (m manageModel) startFleetHost(name string) (manageModel, tea.Cmd) {
	server, ok := m.fleetServer(name)
	if !ok || m.actions.fleetHost == nil || (m.fleet.host.server == name && m.fleet.host.reading) {
		return m, nil
	}
	if m.fleet.host.server != name { // another server's numbers must not stand in for this one's
		m.fleet.host = fleetHostState{server: name}
	}
	m.fleet.host.reading = true
	read, ctx := m.actions.fleetHost, m.ctx
	return m, func() tea.Msg {
		readCtx, cancel := context.WithTimeout(ctx, hostReadTimeout)
		defer cancel()
		status, err := read(readCtx, server)
		return fleetHostReadMsg{server: name, host: status, err: err}
	}
}

// afterFleetHostRead keeps a reading when it is for the server the screen shows.
func (m manageModel) afterFleetHostRead(msg fleetHostReadMsg) manageModel {
	if m.fleet.host.server != msg.server {
		return m
	}
	m.fleet.host.reading, m.fleet.host.err = false, msg.err
	if msg.err == nil {
		m.fleet.host.status, m.fleet.host.read = msg.host, true
	}
	return m
}

// fleetServer is the server of that name in the last round.
func (m manageModel) fleetServer(name string) (fleet.Server, bool) {
	for _, answer := range m.fleet.servers {
		if answer.Server.Name == name {
			return answer.Server, true
		}
	}
	return fleet.Server{}, false
}

// refreshFleetHost reads the machine again, when the screen of a server is open.
func (m manageModel) refreshFleetHost() (manageModel, tea.Cmd) {
	if shown, ok := m.top().(fleetServerScreen); ok {
		return m.startFleetHost(shown.name)
	}
	return m, nil
}

// fleetHostTab is the machine of the open server as the lines of the Server tab.
func (m manageModel) fleetHostTab(name string) manageTab {
	tab := manageTab{title: "Máy", statusCol: -1, historyCol: -1}
	if m.fleet.host.server != name || !m.fleet.host.read {
		return tab
	}
	tab = serverTabOf(hostReportOf(m.fleet.host.status))
	tab.title = "Máy"
	return tab
}

// fleetHostWaitText is what the part says while it has no line.
func (m manageModel) fleetHostWaitText(name string) string {
	state := m.fleet.host
	switch {
	case m.actions.fleetHost == nil:
		return "Không đọc được tình trạng máy."
	case state.server == name && errors.Is(state.err, fleet.ErrNotSupported):
		return "Server này chạy bản Sentinel cũ, chưa có route /api/v1/host: cập nhật Sentinel trên đó."
	case state.server == name && state.err != nil:
		return "Không đọc được tình trạng máy: " + state.err.Error()
	}
	return "Đang đọc tình trạng máy…"
}

// hostReportOf turns what a server sent into the reading the Server tab is built from.
func hostReportOf(status agentapi.HostStatus) app.HostReport {
	state := host.State{
		Hostname: status.Hostname, OS: status.OS, Kernel: status.Kernel,
		Uptime: time.Duration(status.UptimeSeconds) * time.Second, Load: status.Load, CPUs: status.CPUs, CPUPercent: status.CPUPercent,
		Memory: host.Memory{
			TotalBytes: status.Memory.TotalBytes, AvailableBytes: status.Memory.AvailableBytes,
			SwapTotalBytes: status.Memory.SwapTotalBytes, SwapFreeBytes: status.Memory.SwapFreeBytes,
		},
		Systemd: status.Systemd, FailedUnits: unitsOf(status.FailedServices),
	}
	for _, disk := range status.Disks {
		state.Disks = append(state.Disks, host.Disk{Mount: disk.Mount, Device: disk.Device, FSType: disk.FSType, FreeBytes: disk.FreeBytes, TotalBytes: disk.TotalBytes})
	}
	for _, found := range status.Interfaces {
		state.Interfaces = append(state.Interfaces, host.Interface{
			Name: found.Name, RxBytes: found.RxBytes, TxBytes: found.TxBytes, RxErrors: found.RxErrors, TxErrors: found.TxErrors,
			RxDropped: found.RxDropped, TxDropped: found.TxDropped,
		})
	}
	state.TopCPU, state.TopMemory = processesOf(status.TopProcesses.ByCPU), processesOf(status.TopProcesses.ByMemory)
	if status.Sentinel != nil {
		state.Sentinel = unitOf(*status.Sentinel)
	}
	for _, message := range status.Errors {
		state.Errs = append(state.Errs, errors.New(message))
	}
	return app.HostReport{State: state, MinFreePercent: status.MinFreePercent}
}

func unitsOf(services []agentapi.ServiceStatus) []host.Unit {
	var units []host.Unit
	for _, service := range services {
		units = append(units, unitOf(service))
	}
	return units
}

func unitOf(service agentapi.ServiceStatus) host.Unit {
	return host.Unit{Name: service.Name, Active: service.Active, Description: service.Description}
}

func processesOf(processes []agentapi.Process) []host.Process {
	var found []host.Process
	for _, proc := range processes {
		found = append(found, host.Process{PID: proc.PID, Name: proc.Name, CPUPercent: proc.CPUPercent, MemoryBytes: proc.MemoryBytes})
	}
	return found
}
