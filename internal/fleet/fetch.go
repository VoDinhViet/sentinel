package fleet

import (
	"context"
	"sync"
	"time"

	"sentinel/internal/status"
)

// DefaultParallel is how many servers are asked at once when the caller has no preference.
const DefaultParallel = 8

// ServerStatus is what one server answered, or why it did not.
type ServerStatus struct {
	Server    Server
	Report    status.Report
	Err       error     // nil when Report is the server's own
	FetchedAt time.Time // when the call ended
}

// Fetch asks every server for its report, at most limit at a time (DefaultParallel when limit is
// not positive). The answers come in the order of servers, whatever order they arrive in. A server
// that fails or is slow does not hold back the others: its entry has Err.
func Fetch(ctx context.Context, servers []Server, limit int) []ServerStatus {
	if limit <= 0 {
		limit = DefaultParallel
	}
	statuses := make([]ServerStatus, len(servers))
	slots := make(chan struct{}, limit)
	var asking sync.WaitGroup
	for i, server := range servers {
		asking.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			statuses[i] = fetchOne(ctx, server)
		})
	}
	asking.Wait()
	return statuses
}

// Poll reads the fleet file at path (DefaultPath when it is "") and asks every server in it. The
// file is read each time, so a server added to it shows up without restarting the screen.
func Poll(ctx context.Context, path string) ([]ServerStatus, error) {
	if path == "" {
		path = DefaultPath()
	}
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return Fetch(ctx, cfg.Servers, DefaultParallel), nil
}

func fetchOne(ctx context.Context, server Server) ServerStatus {
	answer := ServerStatus{Server: server}
	client, err := NewClient(server)
	if err == nil {
		answer.Report, err = client.Status(ctx)
	}
	answer.Err = err
	answer.FetchedAt = time.Now()
	return answer
}

// ReadHost asks one server how its machine is doing. It is a call of its own, not part of Fetch:
// reading a machine runs systemctl there, which is fine for the one server somebody is looking
// at and too much for the whole list every few seconds.
func ReadHost(ctx context.Context, server Server) (HostStatus, error) {
	client, err := NewClient(server)
	if err != nil {
		return HostStatus{}, err
	}
	return client.Host(ctx)
}
