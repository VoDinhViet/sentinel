package notify

import (
	"context"
	"net/http"
	"time"
)

// heartbeatTimeout bounds one heartbeat request: a slow answer must not hold up the daemon's other tasks.
const heartbeatTimeout = 10 * time.Second

// Heartbeat requests endpoint once to say the daemon is alive. Any 2xx answer is success. Like the
// notifiers it follows no redirect, and an error never carries the URL, which usually holds the secret
// that identifies the check.
func Heartbeat(ctx context.Context, endpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return withoutURL(err)
	}
	req.Header.Set("User-Agent", "sentinel-heartbeat")
	resp, err := httpClient.Do(req)
	if err != nil {
		return withoutURL(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &statusError{Code: resp.StatusCode}
	}
	return nil
}
