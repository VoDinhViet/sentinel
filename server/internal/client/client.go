// Package client calls the API of sentinelctl of the control plane. It is what sentinelctl is made of:
// every command is a call here and the printing of the answer.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sentinel/pkg/pki"
	"sentinel/pkg/termtext"
	"sentinel/server/internal/ctlapi"
)

const (
	requestTimeout = 30 * time.Second
	// maxAnswer is the most the server may answer: 1000 audit rows are a few hundred KiB.
	maxAnswer = 4 << 20
)

// ErrSessionEnded means the token is no longer a session: it expired, was logged out, or the password changed.
var ErrSessionEnded = errors.New("the session has ended: run sentinelctl login")

// Error is an answer that is not a success. Message is the server's own words, cleaned of control characters.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Status) }

// Client calls one control plane with one session.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New prepares calls to the control plane in cfg; token is the session ("" before signing in). The
// connection is made only to a server whose CA has the fingerprint of cfg, and never follows a redirect:
// Go would send the token again to the address in the answer.
func New(cfg Config, token string) (*Client, error) {
	host, err := cfg.Validate()
	if err != nil {
		return nil, err
	}
	tlsConfig, err := pki.PinnedTLSConfig(cfg.CAFingerprint, host)
	if err != nil {
		return nil, err
	}
	return &Client{
		base:  strings.TrimRight(cfg.URL, "/"),
		token: token,
		http: &http.Client{
			Timeout:       requestTimeout,
			Transport:     &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// do sends a request and decodes the JSON answer into out (when not nil). A route that needs the session
// answers 401 when it has ended, which becomes ErrSessionEnded.
func (c *Client) do(ctx context.Context, method, route string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+route, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // the URL is known to the caller and has no secret, but says nothing more
		}
		return fmt.Errorf("call the control plane: %w", err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return fmt.Errorf("read the answer: %w", err)
	}
	if len(answer) > maxAnswer {
		return fmt.Errorf("the answer is larger than %d MiB", maxAnswer>>20)
	}
	if resp.StatusCode >= 300 {
		return c.failure(resp, answer)
	}
	if out == nil || len(answer) == 0 {
		return nil
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("the answer is not what the control plane sends: %w", err)
	}
	return nil
}

func (c *Client) failure(resp *http.Response, answer []byte) error {
	if resp.StatusCode == http.StatusUnauthorized && c.token != "" {
		return ErrSessionEnded
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return &Error{Status: resp.StatusCode, Message: "the server redirected the request, which is not followed: use its final address as the url"}
	}
	var body ctlapi.ErrorBody
	message := "no explanation"
	if json.Unmarshal(answer, &body) == nil && body.Error != "" {
		message = termtext.Clean(body.Error)
	}
	return &Error{Status: resp.StatusCode, Message: message}
}

// Login signs in. The client keeps the token for the calls after it, and returns the answer so the
// caller can store it.
func (c *Client) Login(ctx context.Context, password string) (ctlapi.LoginResponse, error) {
	var answer ctlapi.LoginResponse
	err := c.do(ctx, http.MethodPost, ctlapi.RouteLogin, ctlapi.LoginRequest{Password: password}, &answer)
	if err == nil {
		c.token = answer.Token
	}
	return answer, err
}

func (c *Client) Logout(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, ctlapi.RouteLogout, nil, nil)
}

// Passwd changes the password; every session ends, this one too.
func (c *Client) Passwd(ctx context.Context, oldPassword, newPassword string) error {
	return c.do(ctx, http.MethodPost, ctlapi.RoutePasswd, ctlapi.PasswdRequest{Old: oldPassword, New: newPassword}, nil)
}

func (c *Client) Agents(ctx context.Context) ([]ctlapi.Agent, error) {
	var agents []ctlapi.Agent
	err := c.do(ctx, http.MethodGet, ctlapi.RouteAgents, nil, &agents)
	for i := range agents {
		agents[i].Name = termtext.Clean(agents[i].Name)
	}
	return agents, err
}

func (c *Client) AddAgent(ctx context.Context, name string) (ctlapi.AddAgentResponse, error) {
	var added ctlapi.AddAgentResponse
	err := c.do(ctx, http.MethodPost, ctlapi.RouteAgents, ctlapi.AddAgentRequest{Name: name}, &added)
	return added, err
}

func (c *Client) Revoke(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, ctlapi.RouteAgents+"/"+url.PathEscape(name), nil, nil)
}

// Audit is the newest limit records, newest first.
func (c *Client) Audit(ctx context.Context, limit int) ([]ctlapi.AuditRecord, error) {
	var records []ctlapi.AuditRecord
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s?limit=%d", ctlapi.RouteAudit, limit), nil, &records)
	for i := range records {
		records[i].Target = termtext.Clean(records[i].Target)
		records[i].Detail = termtext.Clean(records[i].Detail)
	}
	return records, err
}

func (c *Client) VerifyAudit(ctx context.Context) (ctlapi.AuditVerify, error) {
	var verified ctlapi.AuditVerify
	err := c.do(ctx, http.MethodGet, ctlapi.RouteAuditVerify, nil, &verified)
	return verified, err
}
