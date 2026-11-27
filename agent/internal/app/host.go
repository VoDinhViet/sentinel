package app

import (
	"context"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/host"
	"sentinel/pkg/agentapi"
)

// HostReport is how the machine is doing now, with the share of free disk space below which
// Sentinel itself raises a disk alert, so a screen can colour the disks by the same rule.
type HostReport struct {
	host.State
	MinFreePercent int
}

// Host reads the state of the machine Sentinel runs on.
func (s *Service) Host(ctx context.Context) HostReport {
	minFree := s.cfg.Disk.MinFree()
	if minFree == 0 { // the alert is off, the screen still needs a line between full and fine
		minFree = config.DefaultMinFreePercent
	}
	return HostReport{State: host.Read(ctx), MinFreePercent: minFree}
}

// Status is the reading in the form of the wire (see agentapi): what the daemon's API answers and what
// an agent reports. host.State is not sent as it is: it has no JSON tags, holds a time.Duration and a
// list of errors, and its fields would become the contract. Text that systemd or the kernel supplied is
// cleaned of control characters.
func (r HostReport) Status() agentapi.HostStatus {
	status := agentapi.HostStatus{
		Hostname: r.Hostname, OS: r.OS, Kernel: r.Kernel,
		UptimeSeconds: int64(r.Uptime.Seconds()), Load: r.Load, CPUs: r.CPUs, CPUPercent: r.CPUPercent,
		Memory: agentapi.MemoryStatus{
			TotalBytes: r.Memory.TotalBytes, AvailableBytes: r.Memory.AvailableBytes,
			SwapTotalBytes: r.Memory.SwapTotalBytes, SwapFreeBytes: r.Memory.SwapFreeBytes,
		},
		TopProcesses: agentapi.TopProcesses{ByCPU: processesOf(r.TopCPU), ByMemory: processesOf(r.TopMemory)},
		Systemd:      r.Systemd, MinFreePercent: r.MinFreePercent,
	}
	for _, disk := range r.Disks {
		status.Disks = append(status.Disks, agentapi.DiskStatus{
			Mount: disk.Mount, Device: disk.Device, FSType: disk.FSType, FreeBytes: disk.FreeBytes, TotalBytes: disk.TotalBytes,
		})
	}
	for _, found := range r.Interfaces {
		status.Interfaces = append(status.Interfaces, agentapi.NetInterface{
			Name: found.Name, RxBytes: found.RxBytes, TxBytes: found.TxBytes,
			RxErrors: found.RxErrors, TxErrors: found.TxErrors, RxDropped: found.RxDropped, TxDropped: found.TxDropped,
		})
	}
	if r.Systemd { // sentinel.service means nothing without systemd
		sentinel := serviceOf(r.Sentinel)
		status.Sentinel = &sentinel
	}
	for _, unit := range r.FailedUnits {
		status.FailedServices = append(status.FailedServices, serviceOf(unit))
	}
	for _, err := range r.Errs {
		status.Errors = append(status.Errors, err.Error())
	}
	return status.Clean()
}

func processesOf(processes []host.Process) []agentapi.Process {
	var wire []agentapi.Process
	for _, proc := range processes {
		wire = append(wire, agentapi.Process{PID: proc.PID, Name: proc.Name, CPUPercent: proc.CPUPercent, MemoryBytes: proc.MemoryBytes})
	}
	return wire
}

func serviceOf(unit host.Unit) agentapi.ServiceStatus {
	return agentapi.ServiceStatus{Name: unit.Name, Active: unit.Active, Description: unit.Description}
}
