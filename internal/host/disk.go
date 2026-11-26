package host

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/shirou/gopsutil/v4/disk"
)

// Disk is one mounted file system and how much of it is free.
type Disk struct {
	Mount      string
	Device     string
	FSType     string
	FreeBytes  uint64
	TotalBytes uint64
}

// FreePercent is the share of the disk still free; 0 for a disk that reports no size.
func (d Disk) FreePercent() float64 {
	if d.TotalBytes == 0 {
		return 0
	}
	return float64(d.FreeBytes) / float64(d.TotalBytes) * 100
}

// readOnly says nothing can be written to the mount (an ISO image, a mounted tool bundle): it is full
// by design, so judging it by free space would only raise a false alarm.
func readOnly(partition disk.PartitionStat) bool { return slices.Contains(partition.Opts, "ro") }

// readDisks lists the physical, writable file systems with their usage, one per device (a bind mount
// of the same device is not another disk), shortest mount point first. A disk that cannot be read, such as
// a stale network share, is left out and reported in errs.
func readDisks(ctx context.Context) (disks []Disk, errs []error) {
	partitions, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil, []error{fmt.Errorf("listing disks: %w", err)}
	}
	slices.SortStableFunc(partitions, func(a, b disk.PartitionStat) int { return strings.Compare(a.Mountpoint, b.Mountpoint) })
	deviceSeen := map[string]bool{}
	for _, partition := range partitions {
		if deviceSeen[partition.Device] || readOnly(partition) {
			continue
		}
		deviceSeen[partition.Device] = true
		usage, err := disk.UsageWithContext(ctx, partition.Mountpoint)
		if err != nil {
			errs = append(errs, fmt.Errorf("disk %s: %w", partition.Mountpoint, err))
			continue
		}
		disks = append(disks, Disk{Mount: partition.Mountpoint, Device: partition.Device, FSType: partition.Fstype, FreeBytes: usage.Free, TotalBytes: usage.Total})
	}
	return disks, errs
}
