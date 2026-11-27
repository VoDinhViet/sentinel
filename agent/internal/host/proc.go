package host

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// topCount is how many processes each ranking keeps.
const topCount = 5

// Process is one process and what it uses. Only its name is read: the command line and the
// environment of a process hold passwords, and nothing here asks for them.
type Process struct {
	PID         int32
	Name        string
	CPUPercent  float64 // of one CPU: 200 is two busy CPUs
	MemoryBytes uint64  // resident set size
}

// processKey names a process for good: a pid alone is reused once the process exits.
type processKey struct {
	pid     int32
	started int64 // start time, milliseconds since the epoch
}

// cpuTimes remembers the CPU seconds each process had at the previous reading, so the next one can
// tell how busy it was in between.
var cpuTimes = struct {
	sync.Mutex
	at      time.Time
	seconds map[processKey]float64
}{}

// readProcesses returns the processes using the most CPU and the most memory. The CPU share is how
// busy a process was since the previous call (since it started, for the first one).
func readProcesses(ctx context.Context) (byCPU, byMemory []Process, err error) {
	all, err := collectProcesses(ctx)
	if err != nil {
		return nil, nil, err
	}
	byCPU, byMemory = rankProcesses(all, topCount)
	return byCPU, byMemory, nil
}

// collectProcesses reads every process and updates the CPU times the next reading compares with.
func collectProcesses(ctx context.Context) ([]Process, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	now := time.Now()
	cpuTimes.Lock()
	defer cpuTimes.Unlock()
	previous, previousAt := cpuTimes.seconds, cpuTimes.at
	seen := make(map[processKey]float64, len(processes))

	var all []Process
	for _, proc := range processes {
		started, err := proc.CreateTimeWithContext(ctx)
		if err != nil {
			continue // it exited since the listing
		}
		times, err := proc.TimesWithContext(ctx)
		if err != nil {
			continue
		}
		memory, err := proc.MemoryInfoWithContext(ctx)
		if err != nil {
			continue
		}
		name, err := proc.NameWithContext(ctx)
		if err != nil {
			continue
		}
		key := processKey{pid: proc.Pid, started: started}
		used := times.User + times.System
		seen[key] = used
		all = append(all, Process{
			PID: proc.Pid, Name: name, MemoryBytes: memory.RSS,
			CPUPercent: cpuShare(used, previous, key, now.Sub(previousAt), now.Sub(time.UnixMilli(started))),
		})
	}
	cpuTimes.seconds, cpuTimes.at = seen, now
	return all, nil
}

// cpuShare is how much CPU a process used, in percent of one CPU. With a previous reading of the same
// process it is the use since then; otherwise the use since the process started.
func cpuShare(used float64, previous map[processKey]float64, key processKey, sincePrevious, sinceStart time.Duration) float64 {
	before, known := previous[key]
	if !known || sincePrevious <= 0 {
		before, sincePrevious = 0, sinceStart
	}
	if sincePrevious <= 0 || used < before {
		return 0
	}
	return (used - before) / sincePrevious.Seconds() * 100
}

// rankProcesses picks the n busiest and the n biggest. A process that uses no CPU is not among the
// busiest: on an idle machine the list would be n processes chosen by nothing.
func rankProcesses(all []Process, n int) (byCPU, byMemory []Process) {
	byCPU = slices.Clone(all)
	byCPU = slices.DeleteFunc(byCPU, func(proc Process) bool { return proc.CPUPercent <= 0 })
	slices.SortStableFunc(byCPU, func(a, b Process) int { return compareDescending(a.CPUPercent, b.CPUPercent) })
	byMemory = slices.Clone(all)
	slices.SortStableFunc(byMemory, func(a, b Process) int { return compareDescending(float64(a.MemoryBytes), float64(b.MemoryBytes)) })
	return byCPU[:min(n, len(byCPU))], byMemory[:min(n, len(byMemory))]
}

func compareDescending(a, b float64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}
