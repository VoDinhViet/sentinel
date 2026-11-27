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
