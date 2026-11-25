package uptime

import (
	"context"
	"fmt"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

const (
	pingCount    = 3
	pingInterval = 300 * time.Millisecond
)

func probePing(ctx context.Context, host string) error {
	stats, err := pingHost(ctx, host)
	if err != nil {
		return err
	}
	if stats.PacketsRecv == 0 {
		return fmt.Errorf("no reply from %s (%d packets sent)", host, stats.PacketsSent)
	}
	return nil
}

// pingHost pings with raw ICMP, which needs CAP_NET_RAW (granted by the systemd unit), and falls back
// to unprivileged ICMP datagram sockets when that fails.
func pingHost(ctx context.Context, host string) (*probing.Statistics, error) {
	pinger, err := newPinger(ctx, host, true)
	if err != nil {
		return nil, err
	}
	if pinger.RunWithContext(ctx) == nil {
		return pinger.Statistics(), nil
	}
	pinger, err = newPinger(ctx, host, false)
	if err != nil {
		return nil, err
	}
	if err := pinger.RunWithContext(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w (grant CAP_NET_RAW or allow unprivileged ping)", err)
	}
	return pinger.Statistics(), nil
}

// newPinger prepares a pinger that gives up when ctx does.
func newPinger(ctx context.Context, host string, privileged bool) (*probing.Pinger, error) {
	pinger, err := probing.NewPinger(host)
	if err != nil {
		return nil, err
	}
	pinger.Count, pinger.Interval = pingCount, pingInterval
	if deadline, ok := ctx.Deadline(); ok {
		pinger.Timeout = time.Until(deadline)
	}
	pinger.SetPrivileged(privileged)
	return pinger, nil
}
