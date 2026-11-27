// Package transport is how the agent talks to the control plane: joining it with a join token, and
// calling it with the certificate it got. The daemon's report loop will be built on the same client.
package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"sentinel/pkg/agentapi"
)

// Files of the identity of the agent, in its directory. They are under the state directory, not /etc,
// because the unit's ProtectSystem=full makes /etc read-only.
const (
	DirName  = "agent"
	CertFile = "cert.pem"
	KeyFile  = "key.pem"
	CAFile   = "ca.pem"

	requestTimeout = 30 * time.Second
	// maxAnswer is the most a control plane may answer to a join: a certificate and a CA are a few KiB.
	maxAnswer = 1 << 20
)

// ErrJoinRefused is the answer to a join token that is wrong, used or expired.
var ErrJoinRefused = errors.New("the join token was refused: it is wrong, already used or expired (ask for a new one: sentinelctl server add)")

// address is the base URL of a control plane and the host its certificate must be for.
type address struct {
	base string
	host string
}

// parseAddress accepts https://host[:port] and nothing else: no user, no path, no query.
func parseAddress(raw string) (address, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return address{}, fmt.Errorf("url %q must look like https://control-plane.example.com:9320", raw)
	}
	return address{base: "https://" + parsed.Host, host: parsed.Hostname()}, nil
}

// newHTTPClient never follows a redirect: the request has a join token or a certificate in it.
func newHTTPClient(config *tls.Config) *http.Client {
	return &http.Client{
		Timeout:       requestTimeout,
		Transport:     &http.Transport{TLSClientConfig: config, Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// call sends a request with a JSON body (when body is not nil) and decodes a JSON answer into out. A
// status that is not a success (2xx) is returned as a *statusError.
func call(ctx context.Context, client *http.Client, method, target string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("call the control plane: %w", err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return fmt.Errorf("read the answer: %w", err)
	}
	if len(answer) > maxAnswer {
		return errors.New("the answer is larger than 1 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newStatusError(resp.StatusCode, answer)
	}
	if out == nil || len(answer) == 0 { // a 204, or an answer nobody reads
		return nil
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("the answer is not what a control plane sends: %w", err)
	}
	return nil
}

// statusError is an answer other than 200.
type statusError struct {
	status  int
	message string
}

func newStatusError(status int, answer []byte) *statusError {
	var body struct {
		Error string `json:"error"`
	}
	message := "no explanation"
	if json.Unmarshal(answer, &body) == nil && body.Error != "" {
		message = body.Error
	}
	return &statusError{status: status, message: message}
}

func (e *statusError) Error() string { return fmt.Sprintf("%s (%d)", e.message, e.status) }

// IsStatus says whether err is the control plane answering with this HTTP status.
func IsStatus(err error, status int) bool {
	var answer *statusError
	return errors.As(err, &answer) && answer.status == status
}

// WhoAmI asks the control plane who it takes this agent for, with the certificate in dir. It proves the
// certificate works end to end: signed by the CA the control plane has, and not revoked.
func WhoAmI(ctx context.Context, dir, rawURL string) (agentapi.WhoAmI, error) {
	client, err := NewClient(dir, rawURL)
	if err != nil {
		return agentapi.WhoAmI{}, err
	}
	return client.WhoAmI(ctx)
}
