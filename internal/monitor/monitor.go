// Package monitor samples CPU, memory, swap, load and disk usage and alerts
// when a value stays above its threshold for the configured duration.
package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	"sentinel/internal/alert"
	"sentinel/internal/config"
	"sentinel/internal/notify"
	"sentinel/internal/store"
)

type Sample struct {
	At     time.Time
	CPU    float64 // percent, all cores
	Memory float64 // percent used
	Swap   float64 // percent used; 0 when there is no swap
	Load1  float64
	Cores  int
	Disks  map[string]float64 // mountpoint -> percent used
}

type Source interface {
	Sample(ctx context.Context) (Sample, error)
}

// System reads the local machine via gopsutil.
type System struct{}

// pseudo filesystems that are never worth alerting on.
var ignoredFS = map[string]bool{
	"squashfs": true, "overlay": true, "tmpfs": true, "devtmpfs": true, "iso9660": true,
	"nsfs": true, "tracefs": true, "fuse.snapfuse": true,
}

func (System) Sample(ctx context.Context) (Sample, error) {
	s := Sample{At: time.Now(), Disks: map[string]float64{}}
	// Blocks for one second to measure CPU over a real interval.
	pct, err := cpu.PercentWithContext(ctx, time.Second, false)
	if err != nil {
		return s, fmt.Errorf("cpu: %w", err)
	}
	if len(pct) > 0 {
		s.CPU = pct[0]
	}
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return s, fmt.Errorf("memory: %w", err)
	}
	s.Memory = vm.UsedPercent
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil && sw.Total > 0 {
		s.Swap = sw.UsedPercent
	}
	if avg, err := load.AvgWithContext(ctx); err == nil {
		s.Load1 = avg.Load1
	}
	if s.Cores, err = cpu.CountsWithContext(ctx, true); err != nil || s.Cores < 1 {
		s.Cores = 1
	}
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return s, fmt.Errorf("disk partitions: %w", err)
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if ignoredFS[p.Fstype] || seen[p.Device] || strings.HasPrefix(p.Mountpoint, "/snap/") {
			continue
		}
		seen[p.Device] = true
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		s.Disks[p.Mountpoint] = u.UsedPercent
	}
	return s, nil
}

type Monitor struct {
	Thresholds config.Thresholds
	Source     Source
	Alerts     *alert.Manager
	Store      *store.Store // optional

	// above records since when each key has been continuously over its threshold.
	above map[string]time.Time
}

// Tick takes one sample, stores it and updates alerts. It is not safe for
// concurrent use; the scheduler never overlaps runs of the same task.
func (m *Monitor) Tick(ctx context.Context) {
	s, err := m.Source.Sample(ctx)
	if err != nil {
		slog.Error("sample metrics", "error", err)
		return
	}
	if m.Store != nil {
		var diskMax float64
		for _, v := range s.Disks {
			diskMax = max(diskMax, v)
		}
		err := m.Store.RecordMetrics(ctx, store.Metrics{At: s.At, CPU: s.CPU, Memory: s.Memory, Swap: s.Swap, Load1: s.Load1, DiskMax: diskMax})
		if err != nil {
			slog.Error("record metrics", "error", err)
		}
	}
	m.Evaluate(ctx, s)
}

// Evaluate compares a sample with the thresholds and fires or resolves alerts.
func (m *Monitor) Evaluate(ctx context.Context, s Sample) {
	if m.above == nil {
		m.above = map[string]time.Time{}
	}
	th := m.Thresholds
	m.check(ctx, s.At, "cpu", "CPU usage", s.CPU, "%", th.CPU)
	m.check(ctx, s.At, "memory", "Memory usage", s.Memory, "%", th.Memory)
	m.check(ctx, s.At, "load", fmt.Sprintf("Load per core (%d cores)", s.Cores), s.Load1/float64(s.Cores), "", th.LoadPerCore)
	for _, mount := range slices.Sorted(maps.Keys(s.Disks)) {
		m.check(ctx, s.At, "disk:"+mount, "Disk usage on "+mount, s.Disks[mount], "%", th.Disk)
	}
}

func (m *Monitor) check(ctx context.Context, at time.Time, key, label string, value float64, unit string, th *config.Threshold) {
	if th == nil {
		return
	}
	key = "monitor:" + key
	if value < th.Value {
		delete(m.above, key)
		m.Alerts.Resolve(ctx, key, label+" back to normal", fmt.Sprintf("Now %.1f%s.", value, unit))
		return
	}
	since, ok := m.above[key]
	if !ok {
		since = at
		m.above[key] = at
	}
	if at.Sub(since) < th.For {
		return
	}
	limit := fmt.Sprintf("%g%s", th.Value, unit)
	if th.For > 0 {
		limit += " for " + th.For.String()
	}
	m.Alerts.Fire(ctx, key, notify.Warning, label+" high",
		fmt.Sprintf("%s is %.1f%s (threshold %s).", label, value, unit, limit))
}
