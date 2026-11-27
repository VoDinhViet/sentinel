// Package host reads how the machine Sentinel runs on is doing: load, CPU, memory, disks, network,
// the busiest processes and the systemd units that failed. It only reads; nothing here changes the machine. The numbers come from
// gopsutil; systemd is asked through systemctl.
package host

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	psutilhost "github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// Memory is the RAM and swap of the machine, in bytes.
type Memory struct {
	TotalBytes, AvailableBytes    uint64
	SwapTotalBytes, SwapFreeBytes uint64
}

// State is one reading of the machine. A part that could not be read is left empty and described
// in Errs, so one missing value does not blank the rest.
type State struct {
	Hostname string
	OS       string // e.g. "ubuntu 24.04"
	Kernel   string
	Uptime   time.Duration
	Load     [3]float64 // the 1, 5 and 15 minute load average
	CPUs     int
	// CPUPercent is how busy the CPUs were since the previous Read (since the process started, for
	// the first one).
	CPUPercent float64
	Memory     Memory
	Disks      []Disk
	Interfaces []Interface
	// TopCPU and TopMemory are the processes that use the most of each, the busiest first.
	TopCPU, TopMemory []Process
	// Systemd says systemctl runs here; Sentinel and FailedUnits mean nothing without it.
	Systemd     bool
	Sentinel    Unit // sentinel.service
	FailedUnits []Unit
	Errs        []error
}

// Read looks at the machine now.
func Read(ctx context.Context) State {
	state := State{CPUs: runtime.NumCPU()}
	note := func(what string, err error) {
		if err != nil {
			state.Errs = append(state.Errs, fmt.Errorf("reading %s: %w", what, err))
		}
	}

	info, err := psutilhost.InfoWithContext(ctx)
	note("host info", err)
	if info != nil {
		state.Hostname, state.Kernel = info.Hostname, info.KernelVersion
		state.OS = strings.TrimSpace(info.Platform + " " + info.PlatformVersion)
		state.Uptime = time.Duration(info.Uptime) * time.Second
	}
	average, err := load.AvgWithContext(ctx)
	note("load", err)
	if average != nil {
		state.Load = [3]float64{average.Load1, average.Load5, average.Load15}
	}
	busy, err := cpu.PercentWithContext(ctx, 0, false)
	note("cpu", err)
	if len(busy) > 0 {
		state.CPUPercent = busy[0]
	}
	virtual, err := mem.VirtualMemoryWithContext(ctx)
	note("memory", err)
	if virtual != nil {
		state.Memory.TotalBytes, state.Memory.AvailableBytes = virtual.Total, virtual.Available
	}
	swap, err := mem.SwapMemoryWithContext(ctx)
	note("swap", err)
	if swap != nil {
		state.Memory.SwapTotalBytes, state.Memory.SwapFreeBytes = swap.Total, swap.Free
	}

	var diskErrs []error
	state.Disks, diskErrs = readDisks(ctx)
	state.Errs = append(state.Errs, diskErrs...)
	var netErr, procErr error
	state.Interfaces, netErr = readInterfaces(ctx)
	state.TopCPU, state.TopMemory, procErr = readProcesses(ctx)
	state.Errs = append(state.Errs, slices.DeleteFunc([]error{netErr, procErr}, func(err error) bool { return err == nil })...)
	var unitErr error
	state.Systemd, state.Sentinel, state.FailedUnits, unitErr = readUnits(ctx)
	if unitErr != nil {
		state.Errs = append(state.Errs, unitErr)
	}
	return state
}
