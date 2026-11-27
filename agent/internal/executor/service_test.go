package executor

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"sentinel/agent/internal/config"
	"sentinel/pkg/agentapi"
)

// systemctlLog is a fake systemctl: it records the arguments of every call and answers from a table.
type systemctlLog struct {
	mu     sync.Mutex
	calls  [][]string
	failOn string // the verb that exits non-zero, with its message
	active string // what is-active says
}

func (s *systemctlLog) run(_ context.Context, args ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slices.Clone(args))
	if slices.Contains(args, "is-active") {
		if s.active != "active" {
			return []byte(s.active + "\n"), errors.New("exit status 3") // systemctl exits 3 for a unit that is not active
		}
		return []byte("active\n"), nil
	}
	if s.failOn != "" && slices.Contains(args, s.failOn) {
		return []byte("Failed to restart nginx.service: Unit nginx.service not found.\n"), errors.New("exit status 5")
	}
	return nil, nil
}

func serviceExecutor(fake *systemctlLog, units ...string) *Executor {
	e := loose(&fakeBackend{}, config.Allow{Services: units})
	e.Systemctl = fake.run
	return e
}

func TestAServiceTaskRunsSystemctlWithFixedArgumentsAndTheUnitAfterTheDashes(t *testing.T) {
	for _, verb := range []string{"start", "stop", "restart"} {
		fake := &systemctlLog{active: "active"}
		if verb == "stop" {
			fake.active = "inactive"
		}
		result, lines := run(serviceExecutor(fake, "nginx.service"), task("service."+verb, map[string]string{"unit": "nginx.service"}))
		if result.State != agentapi.TaskSucceeded || !strings.Contains(result.Output, "now "+fake.active) {
			t.Errorf("%s: %+v", verb, result)
		}
		want := [][]string{
			{"--no-ask-password", "--no-pager", verb, "--", "nginx.service"},
			{"--no-pager", "is-active", "--", "nginx.service"},
		}
		if !slices.EqualFunc(fake.calls, want, slices.Equal) {
			t.Errorf("%s: systemctl was called as %q, want %q", verb, fake.calls, want)
		}
		if len(lines) == 0 {
			t.Errorf("%s: no progress line", verb)
		}
	}
}

func TestAUnitThatIsNotListedIsRefusedBeforeSystemctlIsCalled(t *testing.T) {
	fake := &systemctlLog{active: "active"}
	e := serviceExecutor(fake, "nginx.service")
	for name, unit := range map[string]string{"another unit": "postgresql.service", "the agent": agentapi.OwnUnit, "a flag": "--user.service", "a prefix": "nginx.service.d"} {
		result, _ := run(e, task(agentapi.ActionServiceRestart, map[string]string{"unit": unit}))
		if result.State != agentapi.TaskRefused {
			t.Errorf("%s: %+v", name, result)
		}
	}
	if len(fake.calls) != 0 {
		t.Errorf("a refused task still ran systemctl: %q", fake.calls)
	}
	result, _ := run(e, task(agentapi.ActionServiceRestart, map[string]string{"unit": "postgresql.service"}))
	if !strings.Contains(result.Error, "control_plane.allow.services: [postgresql.service]") {
		t.Errorf("the refusal does not say what to write: %q", result.Error)
	}
}

func TestASystemctlFailureAndAFailedUnitEndTheTaskAsFailed(t *testing.T) {
	broken := &systemctlLog{active: "active", failOn: "restart"}
	result, _ := run(serviceExecutor(broken, "nginx.service"), task(agentapi.ActionServiceRestart, map[string]string{"unit": "nginx.service"}))
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "not found") {
		t.Errorf("systemctl failed: %+v", result)
	}
	crashed := &systemctlLog{active: "failed"}
	result, _ = run(serviceExecutor(crashed, "nginx.service"), task(agentapi.ActionServiceStart, map[string]string{"unit": "nginx.service"}))
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "is failed after start") {
		t.Errorf("the unit failed after start: %+v", result)
	}
	// A oneshot unit that ran and ended is inactive afterwards: that is not a failure.
	oneshot := &systemctlLog{active: "inactive"}
	result, _ = run(serviceExecutor(oneshot, "backup.service"), task(agentapi.ActionServiceRestart, map[string]string{"unit": "backup.service"}))
	if result.State != agentapi.TaskSucceeded {
		t.Errorf("a oneshot unit: %+v", result)
	}
}

// dockerAt serves the Docker Engine API on a unix socket and records the requests it gets.
func dockerAt(t *testing.T, handler http.HandlerFunc) (socket string, requests *[]string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sd") // a short path: a unix socket path is limited to about 100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket = filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	var mu sync.Mutex
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		handler(w, r)
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return socket, &seen
}

func containerExecutor(socket string, names ...string) *Executor {
	return loose(&fakeBackend{}, config.Allow{Containers: names, DockerSocket: socket})
}

func TestAContainerTaskPostsRestartToTheDockerSocketAndNothingElse(t *testing.T) {
	socket, requests := dockerAt(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	e := containerExecutor(socket, "web.1", "worker")
	result, _ := run(e, task(agentapi.ActionContainerRestart, map[string]string{"container": "web.1", "timeout_s": "30"}))
	if result.State != agentapi.TaskSucceeded || !strings.Contains(result.Output, "web.1") {
		t.Errorf("result = %+v", result)
	}
	result, _ = run(e, task(agentapi.ActionContainerRestart, map[string]string{"container": "worker"}))
	if result.State != agentapi.TaskSucceeded {
		t.Errorf("result = %+v", result)
	}
	if want := []string{"POST /containers/web.1/restart?t=30", "POST /containers/worker/restart"}; !slices.Equal(*requests, want) {
		t.Errorf("Docker was asked %q, want %q", *requests, want)
	}
}

func TestAContainerThatIsNotListedNeverReachesDocker(t *testing.T) {
	socket, requests := dockerAt(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	e := containerExecutor(socket, "web")
	for _, name := range []string{"db", "web2", "../containers/web", "web/restart", "web?force=1"} {
		result, _ := run(e, task(agentapi.ActionContainerRestart, map[string]string{"container": name}))
		if result.State != agentapi.TaskRefused {
			t.Errorf("%q: %+v", name, result)
		}
	}
	if len(*requests) != 0 {
		t.Errorf("Docker was reached: %q", *requests)
	}
}

func TestDockerErrorsAreSaidInWordsAndMasked(t *testing.T) {
	socket, _ := dockerAt(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/gone/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such container: gone"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"driver failed: password=hunter2\nsecond line"}`))
	})
	e := containerExecutor(socket, "gone", "web")
	missing, _ := run(e, task(agentapi.ActionContainerRestart, map[string]string{"container": "gone"}))
	if missing.State != agentapi.TaskFailed || !strings.Contains(missing.Error, "no container named gone") {
		t.Errorf("404: %+v", missing)
	}
	broken, _ := run(e, task(agentapi.ActionContainerRestart, map[string]string{"container": "web"}))
	if broken.State != agentapi.TaskFailed || !strings.Contains(broken.Error, "500") || strings.Contains(broken.Error, "hunter2") || strings.Contains(broken.Error, "\n") {
		t.Errorf("500: %+v", broken)
	}
}

func TestADockerSocketThatIsNotThereFailsTheTask(t *testing.T) {
	e := containerExecutor(filepath.Join(t.TempDir(), "nothing.sock"), "web")
	result, _ := run(e, task(agentapi.ActionContainerRestart, map[string]string{"container": "web"}))
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "cannot reach Docker") {
		t.Errorf("result = %+v", result)
	}
}

func TestDockerDoesNotFollowARedirect(t *testing.T) {
	socket, requests := dockerAt(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://169.254.169.254/latest/meta-data/")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	result, _ := run(containerExecutor(socket, "web"), task(agentapi.ActionContainerRestart, map[string]string{"container": "web"}))
	if result.State != agentapi.TaskFailed || len(*requests) != 1 {
		t.Errorf("result = %+v after %d requests", result, len(*requests))
	}
}
