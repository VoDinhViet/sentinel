package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"sentinel/pkg/agentapi"
)

// maxDockerAnswer is how much of an answer of the Docker Engine is read.
const maxDockerAnswer = 4 << 10

// dockerClient speaks HTTP to the Docker Engine over its unix socket. It follows no redirect and uses no
// proxy, and the host in the URL is only a name: every connection goes to the socket.
func dockerClient(socket string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// restartContainer asks Docker to restart one container of the allowlist: POST /containers/{name}/restart.
// The name has the shape the table allows (letters, digits and _ . -), and is escaped as one path segment anyway.
func (e *Executor) restartContainer(ctx context.Context, params map[string]string, log func(string)) agentapi.TaskResult {
	name := params["container"]
	target := "http://docker/containers/" + url.PathEscape(name) + "/restart"
	if seconds := params["timeout_s"]; seconds != "" {
		target += "?t=" + url.QueryEscape(seconds)
	}
	log("asking Docker to restart the container " + name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return failed(err)
	}
	resp, err := dockerClient(e.Allow.Socket()).Do(req)
	if err != nil {
		return failed(fmt.Errorf("cannot reach Docker at %s: %w", e.Allow.Socket(), err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDockerAnswer))
	switch resp.StatusCode {
	case http.StatusNoContent:
		return succeeded("the container " + name + " was restarted")
	case http.StatusNotFound:
		return failed(fmt.Errorf("the Docker Engine has no container named %s", name))
	}
	return failed(fmt.Errorf("the Docker Engine answered %d: %s", resp.StatusCode, dockerMessage(body)))
}

// dockerMessage is the "message" of an error of the Docker Engine, or the start of the body when it is not one.
func dockerMessage(body []byte) string {
	var answer struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &answer) == nil && answer.Message != "" {
		return oneLine(answer.Message)
	}
	return oneLine(strings.TrimSpace(string(body)))
}
