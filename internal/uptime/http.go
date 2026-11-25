package uptime

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"sentinel/internal/config"
)

const (
	userAgent = "sentinel-uptime/1"
	// maxBody bounds how much of a response is searched for the keyword.
	maxBody = 1 << 20
	// maxRedirects is how many redirects a check follows (the limit net/http applies by default).
	maxRedirects = 10
)

var httpTransport = &http.Transport{
	Proxy:             http.ProxyFromEnvironment,
	DisableKeepAlives: true, // measure a fresh connection every time
}

// probeHTTP fetches check.URL and returns the expiry of the certificate of that address.
func probeHTTP(ctx context.Context, check config.UptimeCheck) (time.Time, error) {
	req, err := newRequest(ctx, check.URL)
	if err != nil {
		return time.Time{}, err
	}
	policy := &redirectPolicy{}
	client := &http.Client{Transport: httpTransport, CheckRedirect: policy.check}
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, simplifyErr(err)
	}
	defer resp.Body.Close()
	return leafExpiry(policy.tlsState(resp)), checkResponse(resp, check)
}

func newRequest(ctx context.Context, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		// Go's parse error quotes the whole URL, secrets included; keep only what is wrong with it.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, fmt.Errorf("invalid URL: %w", urlErr.Err)
		}
		return nil, errors.New("invalid URL")
	}
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

// checkResponse compares the status and, when the check has one, the keyword in the body.
func checkResponse(resp *http.Response, check config.UptimeCheck) error {
	if resp.StatusCode != check.ExpectStatus {
		return fmt.Errorf("HTTP %d (expected %d)", resp.StatusCode, check.ExpectStatus)
	}
	if check.Keyword == "" {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if !bytes.Contains(body, []byte(check.Keyword)) {
		return fmt.Errorf("keyword %q not found in response", check.Keyword)
	}
	return nil
}

// redirectPolicy follows redirects, but not from https to http (the site would no longer be shown to be
// served over TLS), and remembers the TLS state of the address in the config: the certificate that is
// watched is that one, not the one of wherever a redirect ends.
type redirectPolicy struct {
	redirected    bool
	configuredTLS *tls.ConnectionState
}

// check is the http.Client.CheckRedirect of a probe.
func (p *redirectPolicy) check(next *http.Request, via []*http.Request) error {
	if len(via) == 1 {
		p.redirected = true
		if next.Response != nil {
			p.configuredTLS = next.Response.TLS
		}
	}
	switch {
	case len(via) >= maxRedirects:
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	case via[0].URL.Scheme == "https" && next.URL.Scheme != "https":
		return errors.New("redirected from https to http: the site is no longer served over TLS")
	}
	return nil
}

// tlsState is the TLS state of the address in the config for the final response.
func (p *redirectPolicy) tlsState(resp *http.Response) *tls.ConnectionState {
	if p.redirected {
		return p.configuredTLS
	}
	return resp.TLS
}

func leafExpiry(state *tls.ConnectionState) time.Time {
	if state == nil || len(state.PeerCertificates) == 0 {
		return time.Time{}
	}
	return state.PeerCertificates[0].NotAfter
}

// simplifyErr strips Go's `Get "url":` prefix, which the alert already names.
func simplifyErr(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Op == "Get" && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}
