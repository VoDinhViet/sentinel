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

// hostEntry is how the machine is doing, in the form of the API: host.State has no JSON tags,
// holds a time.Duration and a list of errors, and its fields would become the contract of the API.
type hostEntry struct {
	Hostname       string      `json:"hostname"`
	OS             string      `json:"os"`
	Kernel         string      `json:"kernel"`
	UptimeSeconds  int64       `json:"uptime_seconds"`
	Load           [3]float64  `json:"load"` // the 1, 5 and 15 minute load average
	CPUs           int         `json:"cpus"`
	CPUPercent     float64     `json:"cpu_percent"`
	Memory         memoryEntry `json:"memory"`
	Disks          []diskEntry `json:"disks"`
	Systemd        bool        `json:"systemd"`
	Sentinel       *unitEntry  `json:"sentinel,omitempty"` // sentinel.service; absent without systemd
	FailedUnits    []unitEntry `json:"failed_units"`
	MinFreePercent int         `json:"min_free_percent"` // below this share of free space a disk is in trouble
	Errors         []string    `json:"errors"`           // the parts that could not be read
}

type memoryEntry struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	SwapFreeBytes  uint64 `json:"swap_free_bytes"`
}

type diskEntry struct {
	Mount      string `json:"mount"`
	Device     string `json:"device"`
	FSType     string `json:"fs_type"`
	FreeBytes  uint64 `json:"free_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

type unitEntry struct {
	Name        string `json:"name"`
	Active      string `json:"active"`
	Description string `json:"description"`
}

// hostEntryOf converts a reading. Text that systemd or the kernel supplied is cleaned of control
// characters, as the terminal screens do.
func hostEntryOf(report app.HostReport) hostEntry {
	entry := hostEntry{
		Hostname: termtext.Clean(report.Hostname), OS: termtext.Clean(report.OS), Kernel: termtext.Clean(report.Kernel),
		UptimeSeconds: int64(report.Uptime.Seconds()), Load: report.Load, CPUs: report.CPUs, CPUPercent: report.CPUPercent,
		Memory: memoryEntry{
			TotalBytes: report.Memory.TotalBytes, AvailableBytes: report.Memory.AvailableBytes,
			SwapTotalBytes: report.Memory.SwapTotalBytes, SwapFreeBytes: report.Memory.SwapFreeBytes,
		},
		Disks: []diskEntry{}, Systemd: report.Systemd, FailedUnits: []unitEntry{},
		MinFreePercent: report.MinFreePercent, Errors: []string{},
	}
	for _, disk := range report.Disks {
		entry.Disks = append(entry.Disks, diskEntry{
			Mount: termtext.Clean(disk.Mount), Device: termtext.Clean(disk.Device), FSType: termtext.Clean(disk.FSType),
			FreeBytes: disk.FreeBytes, TotalBytes: disk.TotalBytes,
		})
	}
	if report.Systemd {
		sentinel := unitEntryOf(report.Sentinel)
		entry.Sentinel = &sentinel
	}
	for _, unit := range report.FailedUnits {
		entry.FailedUnits = append(entry.FailedUnits, unitEntryOf(unit))
	}
	for _, err := range report.Errs {
		entry.Errors = append(entry.Errors, termtext.Clean(err.Error()))
	}
	return entry
}

func unitEntryOf(unit host.Unit) unitEntry {
	return unitEntry{Name: termtext.Clean(unit.Name), Active: termtext.Clean(unit.Active), Description: termtext.Clean(unit.Description)}
}

// host answers how the machine is doing now. The CPU share is measured since the previous reading
// by anyone in this process (the manager screen of the same process shares the counter), so a
// caller that polls gets the load between its own calls.
func (s *Server) host(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, hostEntryOf(s.backend.Host(ctx)))
}
