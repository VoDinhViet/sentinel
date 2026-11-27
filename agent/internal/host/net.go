package host

import (
	"context"
	"fmt"
	"slices"
	"strings"

	psutilnet "github.com/shirou/gopsutil/v4/net"
)

// Interface holds the counters of one network interface since the machine booted. They are raw: a
// rate is two readings and the time between them, which is for whoever keeps the history.
type Interface struct {
	Name                 string
	RxBytes, TxBytes     uint64
	RxErrors, TxErrors   uint64
	RxDropped, TxDropped uint64
}

// skipInterface says an interface is not worth a line: the loopback, and the virtual ends docker and
// other container runtimes create for every container (one more for each one that starts).
func skipInterface(name string) bool {
	return name == "lo" || strings.HasPrefix(name, "veth")
}

// readInterfaces returns the counters of each interface, sorted by name.
func readInterfaces(ctx context.Context) ([]Interface, error) {
	counters, err := psutilnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("reading network: %w", err)
	}
	var interfaces []Interface
	for _, counter := range counters {
		if skipInterface(counter.Name) {
			continue
		}
		interfaces = append(interfaces, Interface{
			Name:    counter.Name,
			RxBytes: counter.BytesRecv, TxBytes: counter.BytesSent,
			RxErrors: counter.Errin, TxErrors: counter.Errout,
			RxDropped: counter.Dropin, TxDropped: counter.Dropout,
		})
	}
	slices.SortFunc(interfaces, func(a, b Interface) int { return strings.Compare(a.Name, b.Name) })
	return interfaces, nil
}
