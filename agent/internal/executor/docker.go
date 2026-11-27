package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"sentinel/pkg/agentapi"
)

// maxDockerAnswer is how much of an answer of the Docker Engine is read.
const (
	maxDockerAnswer  = 4 << 10
	maxInspectAnswer = 1 << 20 // the description of a container is long: its environment is in it, and is not used
)

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

// resolveContainer asks Docker which container the name is, and gives its full id. Docker takes a name in a
// path as an id, a name or the start of an id, whichever matches: the allowlist says a name, so the answer has to
// be that container, not another whose id happens to start with the same letters.
func resolveContainer(ctx context.Context, client *http.Client, name string) (id string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/"+url.PathEscape(name)+"/json", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxInspectAnswer))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", errNoContainer
	default:
		return "", fmt.Errorf("the Docker Engine answered %d: %s", resp.StatusCode, dockerMessage(body))
	}
	var found struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(body, &found); err != nil || found.ID == "" {
		return "", errors.New("the Docker Engine gave an answer that is not a container")
	}
	if found.Name != "/"+name && found.ID != name {
		return "", fmt.Errorf("%s is not the name of a container, only the start of the id of another: the allowlist names containers", name)
	}
	return found.ID, nil
}

// errNoContainer is Docker saying there is no such container.
var errNoContainer = errors.New("no such container")

// restartContainer asks Docker to restart one container of the allowlist: POST /containers/{id}/restart. The
// name has the shape the table allows (letters, digits and _ . -), and is escaped as one path segment anyway.
func (e *Executor) restartContainer(ctx context.Context, params map[string]string, log func(string)) agentapi.TaskResult {
	name := params["container"]
	client := dockerClient(e.Allow.Socket())
	log("looking for the container " + name)
	id, err := resolveContainer(ctx, client, name)
	switch {
	case errors.Is(err, errNoContainer):
		return failed(fmt.Errorf("the Docker Engine has no container named %s", name))
	case err != nil:
		return failed(fmt.Errorf("cannot ask Docker at %s: %w", e.Allow.Socket(), err))
	}
	target := "http://docker/containers/" + url.PathEscape(id) + "/restart"
	if seconds := params["timeout_s"]; seconds != "" {
		target += "?t=" + url.QueryEscape(seconds)
	}
	log("asking Docker to restart the container " + name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return failed(err)
	}
	resp, err := client.Do(req)
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
