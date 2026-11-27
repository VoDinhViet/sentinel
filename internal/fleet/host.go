package fleet

// HostStatus is how one machine is doing, as GET /api/v1/host answers. It is the other end of
// the type of the same shape in internal/api, which a test there keeps in step with this one.
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
	Systemd        bool            `json:"systemd"`
	Sentinel       *ServiceStatus  `json:"sentinel,omitempty"` // sentinel.service; absent without systemd
	FailedServices []ServiceStatus `json:"failed_units"`
	MinFreePercent int             `json:"min_free_percent"`
	Errors         []string        `json:"errors"` // the parts that could not be read
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

// ServiceStatus is one systemd unit.
type ServiceStatus struct {
	Name        string `json:"name"`
	Active      string `json:"active"`
	Description string `json:"description"`
}
