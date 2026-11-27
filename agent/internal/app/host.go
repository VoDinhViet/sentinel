package app

import (
	"context"

	"sentinel/agent/internal/config"
	"sentinel/agent/internal/host"
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
