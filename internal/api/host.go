package api

import (
	"context"
	"net/http"
	"time"

	"sentinel/internal/app"
	"sentinel/internal/host"
	"sentinel/internal/termtext"
)

// hostTimeout ends a reading of the machine (its systemctl calls have their own, shorter limits).
const hostTimeout = 10 * time.Second

// hostStatus is how the machine is doing, in the form of the API. host.State is not sent as it is:
// it has no JSON tags, holds a time.Duration and a list of errors, and its fields would become the
// contract of the API.
type hostStatus struct {
	Hostname       string          `json:"hostname"`
	OS             string          `json:"os"`
	Kernel         string          `json:"kernel"`
	UptimeSeconds  int64           `json:"uptime_seconds"`
	Load           [3]float64      `json:"load"` // the 1, 5 and 15 minute load average
	CPUs           int             `json:"cpus"`
	CPUPercent     float64         `json:"cpu_percent"`
	Memory         memoryStatus    `json:"memory"`
	Disks          []diskStatus    `json:"disks"`
	Systemd        bool            `json:"systemd"`
	Sentinel       *serviceStatus  `json:"sentinel,omitempty"` // sentinel.service; absent without systemd
	FailedServices []serviceStatus `json:"failed_units"`
	MinFreePercent int             `json:"min_free_percent"` // below this share of free space a disk is in trouble
	Errors         []string        `json:"errors"`           // the parts that could not be read
}

type memoryStatus struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	SwapFreeBytes  uint64 `json:"swap_free_bytes"`
}

type diskStatus struct {
	Mount      string `json:"mount"`
	Device     string `json:"device"`
	FSType     string `json:"fs_type"`
	FreeBytes  uint64 `json:"free_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

// serviceStatus is one systemd unit. The API calls the list of failed ones failed_units, as systemd does.
type serviceStatus struct {
	Name        string `json:"name"`
	Active      string `json:"active"`
	Description string `json:"description"`
}

// host answers how the machine is doing now. The CPU share is measured since the previous reading
// by anyone in this process (the manager screen of the same process shares the counter), so a
// caller that polls gets the load between its own calls.
func (s *Server) host(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, hostStatusOf(s.backend.Host(ctx)))
}

// hostStatusOf converts a reading. Text that systemd or the kernel supplied is cleaned of control
// characters, as the terminal screens do.
func hostStatusOf(reading app.HostReport) hostStatus {
	return hostStatus{
		Hostname:       termtext.Clean(reading.Hostname),
		OS:             termtext.Clean(reading.OS),
		Kernel:         termtext.Clean(reading.Kernel),
		UptimeSeconds:  int64(reading.Uptime.Seconds()),
		Load:           reading.Load,
		CPUs:           reading.CPUs,
		CPUPercent:     reading.CPUPercent,
		Memory:         memoryStatusOf(reading.Memory),
		Disks:          diskStatusesOf(reading.Disks),
		Systemd:        reading.Systemd,
		Sentinel:       sentinelServiceOf(reading),
		FailedServices: serviceStatusesOf(reading.FailedUnits),
		MinFreePercent: reading.MinFreePercent,
		Errors:         readingErrorsOf(reading.Errs),
	}
}

func memoryStatusOf(memory host.Memory) memoryStatus {
	return memoryStatus{
		TotalBytes: memory.TotalBytes, AvailableBytes: memory.AvailableBytes,
		SwapTotalBytes: memory.SwapTotalBytes, SwapFreeBytes: memory.SwapFreeBytes,
	}
}

func diskStatusesOf(disks []host.Disk) []diskStatus {
	statuses := []diskStatus{}
	for _, disk := range disks {
		statuses = append(statuses, diskStatus{
			Mount: termtext.Clean(disk.Mount), Device: termtext.Clean(disk.Device), FSType: termtext.Clean(disk.FSType),
			FreeBytes: disk.FreeBytes, TotalBytes: disk.TotalBytes,
		})
	}
	return statuses
}

// sentinelServiceOf is sentinel.service, or nil on a machine without systemd, where it means nothing.
func sentinelServiceOf(reading app.HostReport) *serviceStatus {
	if !reading.Systemd {
		return nil
	}
	service := serviceStatusOf(reading.Sentinel)
	return &service
}

func serviceStatusesOf(units []host.Unit) []serviceStatus {
	statuses := []serviceStatus{}
	for _, unit := range units {
		statuses = append(statuses, serviceStatusOf(unit))
	}
	return statuses
}

func serviceStatusOf(unit host.Unit) serviceStatus {
	return serviceStatus{Name: termtext.Clean(unit.Name), Active: termtext.Clean(unit.Active), Description: termtext.Clean(unit.Description)}
}

func readingErrorsOf(errs []error) []string {
	messages := []string{}
	for _, err := range errs {
		messages = append(messages, termtext.Clean(err.Error()))
	}
	return messages
}
