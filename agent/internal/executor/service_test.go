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
	"time"

	"sentinel/agent/internal/config"
	"sentinel/pkg/agentapi"
)

// systemctlLog is a fake systemctl: it records the arguments of every call and answers from a table.
type systemctlLog struct {
	mu     sync.Mutex
	calls  [][]string
	failOn string // the verb that exits non-zero, with its message
	active string // what is-active says
	alias  string // when set, `show` says every unit is really this one
}

func (s *systemctlLog) run(_ context.Context, args ...string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, slices.Clone(args))
	if slices.Contains(args, "show") {
		if s.alias != "" {
			return []byte(s.alias + "\n"), nil
		}
		return []byte(args[len(args)-1] + "\n"), nil // a unit that is not an alias is itself
	}
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
			{"--no-pager", "show", "--property=Id", "--value", "--", "nginx.service"},
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
	// Even a config that listed the agent's own unit (the file check would have refused it) is not obeyed:
	// the table is checked before the allowlist.
	own := serviceExecutor(fake, agentapi.OwnUnit)
	if result, _ := run(own, task(agentapi.ActionServiceStop, map[string]string{"unit": agentapi.OwnUnit})); result.State != agentapi.TaskRefused || len(fake.calls) != 0 {
		t.Errorf("sentinel.service on a list: %+v, calls %q", result, fake.calls)
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
	socket, requests := dockerAt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet { // the inspect that resolves the name
			name := strings.Split(r.URL.Path, "/")[2]
			_, _ = w.Write([]byte(`{"Id":"` + "id-of-" + name + `","Name":"/` + name + `","Config":{"Env":["DB_PASSWORD=hunter2"]}}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	e := containerExecutor(socket, "web.1", "worker")
	result, _ := run(e, task(agentapi.ActionContainerRestart, map[string]string{"container": "web.1", "timeout_s": "30"}))
	if result.State != agentapi.TaskSucceeded || !strings.Contains(result.Output, "web.1") {
		t.Errorf("result = %+v", result)
	}
	result, _ = run(e, task(agentapi.ActionContainerRestart, map[string]string{"container": "worker"}))
	if result.State != agentapi.TaskSucceeded {
		t.Errorf("result = %+v", result)
	}
	if want := []string{"GET /containers/web.1/json", "POST /containers/id-of-web.1/restart?t=30", "GET /containers/worker/json", "POST /containers/id-of-worker/restart"}; !slices.Equal(*requests, want) {
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
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"Id":"abc123","Name":"/web"}`))
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
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "cannot ask Docker") {
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

func TestAUnitThatIsAnAliasOfTheAgentIsNotTouched(t *testing.T) {
	fake := &systemctlLog{active: "active", alias: agentapi.OwnUnit}
	result, _ := run(serviceExecutor(fake, "backup-agent.service"), task(agentapi.ActionServiceStop, map[string]string{"unit": "backup-agent.service"}))
	if result.State != agentapi.TaskRefused {
		t.Errorf("result = %+v", result)
	}
	for _, call := range fake.calls {
		if slices.Contains(call, "stop") {
			t.Errorf("the alias of the agent was stopped: %q", fake.calls)
		}
	}
	silent := &systemctlLog{active: "active", alias: " "} // systemd said nothing about the unit
	if result, _ := run(serviceExecutor(silent, "x.service"), task(agentapi.ActionServiceStop, map[string]string{"unit": "x.service"})); result.State != agentapi.TaskRefused {
		t.Errorf("a unit systemd cannot name: %+v", result)
	}
}

// runSystemctl is the one part the fakes above skip: it runs a real program, so it is tried with a script
// standing in for systemctl (run the package with -race: the two streams of the program share one buffer).
func TestRunSystemctlCollectsBothStreamsWithoutARaceAndWithACapAndASmallEnvironment(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "systemctl")
	body := "#!/bin/sh\nfor i in 1 2 3 4 5 6 7 8 9 10; do echo \"out $i\"; echo \"err $i\" >&2; done\necho \"env:$SENTINEL_SECRET_FOR_TEST:$LANG\"\nhead -c 20000 /dev/zero | tr '\\0' x\nexit 4\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SENTINEL_SECRET_FOR_TEST", "topsecret")
	defer func(old []string) { systemctlPaths = old }(systemctlPaths)
	systemctlPaths = []string{script}
	output, err := runSystemctl(context.Background(), "restart", "--", "a.service")
	if err == nil || !strings.Contains(err.Error(), "exit status 4") {
		t.Fatalf("err = %v", err)
	}
	if len(output) != maxCommandOutput {
		t.Errorf("%d bytes kept, want the cap of %d", len(output), maxCommandOutput)
	}
	text := string(output)
	if !strings.Contains(text, "out 1") || !strings.Contains(text, "err 10") || !strings.Contains(text, "env::C") || strings.Contains(text, "topsecret") {
		t.Errorf("output = %.200q", text)
	}
	systemctlPaths = []string{filepath.Join(dir, "none")}
	if _, err := runSystemctl(context.Background(), "x"); err == nil {
		t.Error("a missing systemctl was not reported")
	}
}

func TestASystemctlThatHangsEndsWithTheContext(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(old []string) { systemctlPaths = old }(systemctlPaths)
	systemctlPaths = []string{script}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	if _, err := runSystemctl(ctx, "restart", "--", "a.service"); err == nil || time.Since(began) > 10*time.Second {
		t.Errorf("err = %v after %s", err, time.Since(began))
	}
}

// Docker takes the name in a path as an id, a name or the start of an id. "db" on the list means the container
// called db, not whichever container has an id that starts with "db".
func TestANameThatDockerResolvesToAnotherContainerIsNotRestarted(t *testing.T) {
	socket, requests := dockerAt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"Id":"db9f00d5e5","Name":"/payments"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	result, _ := run(containerExecutor(socket, "db"), task(agentapi.ActionContainerRestart, map[string]string{"container": "db"}))
	if result.State != agentapi.TaskFailed || !strings.Contains(result.Error, "start of the id of another") {
		t.Errorf("result = %+v", result)
	}
	if want := []string{"GET /containers/db/json"}; !slices.Equal(*requests, want) {
		t.Errorf("Docker was asked %q: nothing may be restarted", *requests)
	}
}

func TestAFullIdOnTheListIsTheContainerWithThatId(t *testing.T) {
	full := strings.Repeat("ab", 32)
	socket, requests := dockerAt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"Id":"` + full + `","Name":"/web"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	result, _ := run(containerExecutor(socket, full), task(agentapi.ActionContainerRestart, map[string]string{"container": full}))
	if result.State != agentapi.TaskSucceeded || len(*requests) != 2 || !strings.HasPrefix((*requests)[1], "POST /containers/"+full+"/restart") {
		t.Errorf("result = %+v, requests %q", result, *requests)
	}
}
