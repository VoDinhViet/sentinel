// Package agentapi is what an agent and the control plane (and the daemon's own HTTP API) say to each
// other: the shape of the JSON on the wire. It holds types and the rules for reading them, nothing
// that runs on a machine.
package agentapi

import (
	"slices"

	"sentinel/pkg/termtext"
)

// HostStatus is how one machine is doing. Every list is a list, never null, when the agent sends it.
type HostStatus struct {
	Hostname       string          `json:"hostname"`
	OS             string          `json:"os"`
	Kernel         string          `json:"kernel"`
	UptimeSeconds  int64           `json:"uptime_seconds"`
	Load           [3]float64      `json:"load"` // the 1, 5 and 15 minute load average
	CPUs           int             `json:"cpus"`
	CPUPercent     float64         `json:"cpu_percent"`
	Memory         MemoryStatus    `json:"memory"`
	Disks          []DiskStatus    `json:"disks"`
	Interfaces     []NetInterface  `json:"interfaces"`
	TopProcesses   TopProcesses    `json:"top_processes"`
	Systemd        bool            `json:"systemd"`
	Sentinel       *ServiceStatus  `json:"sentinel,omitempty"` // sentinel.service; absent without systemd
	FailedServices []ServiceStatus `json:"failed_units"`
	MinFreePercent int             `json:"min_free_percent"` // below this share of free space a disk is in trouble
	Errors         []string        `json:"errors"`           // the parts that could not be read
}

type MemoryStatus struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	SwapFreeBytes  uint64 `json:"swap_free_bytes"`
}

type DiskStatus struct {
	Mount      string `json:"mount"`
	Device     string `json:"device"`
	FSType     string `json:"fs_type"`
	FreeBytes  uint64 `json:"free_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

// NetInterface holds the counters of one network interface since the machine booted. They are raw:
// whoever wants a rate takes two readings and divides by the time between them.
type NetInterface struct {
	Name      string `json:"name"`
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
	RxErrors  uint64 `json:"rx_errors"`
	TxErrors  uint64 `json:"tx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxDropped uint64 `json:"tx_dropped"`
}

// TopProcesses are the processes that use the most CPU and the most memory. Only a name, never the
// command line or the environment, which hold passwords.
type TopProcesses struct {
	ByCPU    []Process `json:"by_cpu"`
	ByMemory []Process `json:"by_memory"`
}

type Process struct {
	PID         int32   `json:"pid"`
	Name        string  `json:"name"`
	CPUPercent  float64 `json:"cpu_percent"` // of one CPU: 200 is two busy CPUs
	MemoryBytes uint64  `json:"memory_bytes"`
}

// ServiceStatus is one systemd unit. The list of failed ones is called failed_units, as systemd does.
type ServiceStatus struct {
	Name        string `json:"name"`
	Active      string `json:"active"`
	Description string `json:"description"`
}

// Clean returns the status with every text that came from the machine (names, error messages, unit
// descriptions) stripped of escape sequences and control characters, and every list a list, never
// nil. Whoever receives a status from another machine calls it before showing or storing it.
func (h HostStatus) Clean() HostStatus {
	h.Hostname, h.OS, h.Kernel = termtext.Clean(h.Hostname), termtext.Clean(h.OS), termtext.Clean(h.Kernel)
	h.Disks = cleanEach(h.Disks, func(disk DiskStatus) DiskStatus {
		disk.Mount, disk.Device, disk.FSType = termtext.Clean(disk.Mount), termtext.Clean(disk.Device), termtext.Clean(disk.FSType)
		return disk
	})
	h.Interfaces = cleanEach(h.Interfaces, func(netIf NetInterface) NetInterface {
		netIf.Name = termtext.Clean(netIf.Name)
		return netIf
	})
	h.TopProcesses.ByCPU = cleanEach(h.TopProcesses.ByCPU, cleanProcess)
	h.TopProcesses.ByMemory = cleanEach(h.TopProcesses.ByMemory, cleanProcess)
	if h.Sentinel != nil {
		service := cleanService(*h.Sentinel)
		h.Sentinel = &service
	}
	h.FailedServices = cleanEach(h.FailedServices, cleanService)
	h.Errors = cleanEach(h.Errors, termtext.Clean)
	return h
}

// cleanEach returns a new list (never nil) of clean applied to each item, so the input is not changed.
func cleanEach[T any](items []T, clean func(T) T) []T {
	cleaned := make([]T, 0, len(items))
	for _, item := range slices.Clone(items) {
		cleaned = append(cleaned, clean(item))
	}
	return cleaned
}

func cleanProcess(process Process) Process {
	process.Name = termtext.Clean(process.Name)
	return process
}

func cleanService(service ServiceStatus) ServiceStatus {
	service.Name, service.Active, service.Description = termtext.Clean(service.Name), termtext.Clean(service.Active), termtext.Clean(service.Description)
	return service
}
