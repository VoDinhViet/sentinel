package fleet

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"sentinel/agent/internal/status"
)

const (
	// requestTimeout ends a call to one server, whatever it is doing.
	requestTimeout = 15 * time.Second
	// maxBody is the most a server may answer: a report is a few KiB, so more is a fault or an attack.
	maxBody = 1 << 20
)

var (
	// ErrUnauthorized is a server that did not accept the token (it is wrong, or it was changed).
	ErrUnauthorized = errors.New("the token was refused")
	// ErrNotSupported is a server that has no such route: it runs an older Sentinel.
	ErrNotSupported = errors.New("the server does not have this route (an older Sentinel?)")
	// ErrNotSentinel is an answer that is valid JSON but no report: a proxy, or another service, answers at that address.
	ErrNotSentinel = errors.New("the answer is not a Sentinel report")
)

// Client calls the API of one server.
type Client struct {
	server Server
	http   *http.Client
}

// NewClient prepares the calls to a server. It reads the server's tls_ca, so a bad file shows
// here and not at the first call.
func NewClient(server Server) (*Client, error) {
	transport, err := transportFor(server)
	if err != nil {
		return nil, err
	}
	return &Client{server: server, http: httpClientOf(transport)}, nil
}

// httpClientOf never follows a redirect: Go would send the request again, bearer token included,
// to the address in the answer. A redirect is reported as a failure.
func httpClientOf(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Transport:     transport,
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func transportFor(server Server) (*http.Transport, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if server.TLSCA == "" {
		return transport, nil
	}
	pem, err := os.ReadFile(server.TLSCA)
	if err != nil {
		return nil, fmt.Errorf("server %s: tls_ca: %w", server.Name, err)
	}
	authorities := x509.NewCertPool()
	if !authorities.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("server %s: tls_ca %s holds no PEM certificate", server.Name, server.TLSCA)
	}
	transport.TLSClientConfig.RootCAs = authorities
	return transport, nil
}

// Close lets go of the connections kept open for the next call. A client is made for one round of
// calls, so nothing waits for the idle timeout.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Status is the report of the server (GET /api/v1/status), its text cleaned of control characters.
// An answer that has no time of its own ({} or null, which any 200 JSON would be) is not a report.
func (c *Client) Status(ctx context.Context) (status.Report, error) {
	var report status.Report
	err := c.get(ctx, "/api/v1/status", &report)
	if err == nil && report.GeneratedAt.IsZero() {
		err = fmt.Errorf("server %s: %w", c.server.Name, ErrNotSentinel)
	}
	return cleanReport(report), err
}

// Host is how the machine of the server is doing (GET /api/v1/host), its text cleaned too.
func (c *Client) Host(ctx context.Context) (HostStatus, error) {
	var host HostStatus
	err := c.get(ctx, "/api/v1/host", &host)
	return cleanHost(host), err
}

// get reads one JSON answer into target. No error carries the token.
func (c *Client) get(ctx context.Context, route string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.server.URL, "/")+route, nil)
	if err != nil {
		return fmt.Errorf("server %s: %w", c.server.Name, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.server.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("server %s: %w", c.server.Name, withoutURL(err))
	}
	defer resp.Body.Close()
	if err := answerError(c.server.Name, resp); err != nil {
		return err
	}
	return decodeBody(c.server.Name, resp.Body, target)
}

// withoutURL drops the request URL from a failed call's error, which says only what went wrong.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// answerError is nil for 200 and otherwise tells what the status code means.
func answerError(serverName string, resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("server %s: %w", serverName, ErrUnauthorized)
	case http.StatusNotFound:
		return fmt.Errorf("server %s: %w", serverName, ErrNotSupported)
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return fmt.Errorf("server %s: answered %d, a redirect, which is not followed (use the final address as url)", serverName, resp.StatusCode)
	}
	return fmt.Errorf("server %s: answered %d", serverName, resp.StatusCode)
}

func decodeBody(serverName string, body io.Reader, target any) error {
	answer, err := io.ReadAll(io.LimitReader(body, maxBody+1))
	if err != nil {
		return fmt.Errorf("server %s: read the answer: %w", serverName, withoutURL(err))
	}
	if len(answer) > maxBody {
		return fmt.Errorf("server %s: the answer is larger than %d MiB", serverName, maxBody>>20)
	}
	if err := json.Unmarshal(answer, target); err != nil {
		return fmt.Errorf("server %s: %w: %w", serverName, ErrNotSentinel, err)
	}
	return nil
}
