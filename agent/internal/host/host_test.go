package host

import (
	"context"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
)

func TestParseFailedUnits(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []Unit
	}{
		{name: "none", text: ""},
		{
			name: "plain",
			text: "nginx.service loaded failed failed A high performance web server\n",
			want: []Unit{{Name: "nginx.service", Active: "failed", Description: "A high performance web server"}},
		},
		{
			name: "bullet and two units",
			text: "● apt-daily.service loaded failed failed Daily apt download\n● snapd.service  loaded failed failed Snap Daemon\n",
			want: []Unit{
				{Name: "apt-daily.service", Active: "failed", Description: "Daily apt download"},
				{Name: "snapd.service", Active: "failed", Description: "Snap Daemon"},
			},
		},
		{name: "short line is skipped", text: "garbage\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFailedUnits(tt.text)
			if len(got) != len(tt.want) {
				t.Fatalf("units = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("unit %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDiskFreePercent(t *testing.T) {
	if got := (Disk{FreeBytes: 25, TotalBytes: 100}).FreePercent(); got != 25 {
		t.Errorf("FreePercent = %v, want 25", got)
	}
	if got := (Disk{}).FreePercent(); got != 0 {
		t.Errorf("FreePercent of a disk without size = %v, want 0", got)
	}
}

// Read looks at the real machine: whatever it is, the numbers must make sense and nothing may hang.
func TestReadSeesThisMachine(t *testing.T) {
	state := Read(context.Background())
	if state.CPUs <= 0 || state.Memory.TotalBytes == 0 || state.Uptime <= 0 {
		t.Errorf("state = %+v", state)
	}
	if state.Memory.AvailableBytes > state.Memory.TotalBytes {
		t.Errorf("available %d is more than total %d", state.Memory.AvailableBytes, state.Memory.TotalBytes)
	}
	if state.CPUPercent < 0 || state.CPUPercent > 100 {
		t.Errorf("cpu percent = %v", state.CPUPercent)
	}
	for _, found := range state.Disks {
		if found.FreeBytes > found.TotalBytes {
			t.Errorf("disk %s: free %d is more than total %d", found.Mount, found.FreeBytes, found.TotalBytes)
		}
	}
}

func TestReadOnlyMountsAreNotDisksToWatch(t *testing.T) {
	tests := []struct {
		name string
		opts []string
		want bool
	}{
		{"read-write", []string{"rw", "relatime"}, false},
		{"read-only", []string{"ro", "relatime"}, true},
		{"no options", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readOnly(disk.PartitionStat{Opts: tt.opts}); got != tt.want {
				t.Errorf("readOnly(%v) = %v, want %v", tt.opts, got, tt.want)
			}
		})
	}
}
