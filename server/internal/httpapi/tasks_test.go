package httpapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/ctlapi"
)

// event is one server-sent event as the client reads it.
type event struct{ name, id, data string }

// readEvents reads a stream of server-sent events until the server closes it.
func readEvents(t *testing.T, p *plant, token, route, lastID string) []event {
	t.Helper()
	req, _ := http.NewRequest("GET", p.ctl.URL+route, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := p.client(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events: status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var events []event
	var current event
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if current.name != "" {
				events = append(events, current)
			}
			current = event{}
		case strings.HasPrefix(line, "event: "):
			current.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			current.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			current.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return events
}

// queue asks for a task on a server as sentinelctl does, and returns it.
func (p *plant) queue(token, agent, action string, params map[string]string) ctlapi.Task {
	p.t.Helper()
	var created ctlapi.Task
	status, _ := call(p.t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteTasks, token, ctlapi.CreateTaskRequest{Agent: agent, Action: action, Params: params}, &created)
	if status != http.StatusCreated {
		p.t.Fatalf("queue %s: status %d", action, status)
	}
	return created
}

// take is what an agent does to get its tasks.
func (p *plant) take(agent *http.Client, wait string) []agentapi.Task {
	p.t.Helper()
	var tasks []agentapi.Task
	if status, _ := call(p.t, agent, "GET", p.agents.URL+agentapi.RouteTasks+"?wait="+wait, "", nil, &tasks); status != http.StatusOK {
		p.t.Fatalf("poll: status %d", status)
	}
	return tasks
}

func taskRoute(base string, id int64, suffix string) string {
	return fmt.Sprintf("%s/v1/agent/tasks/%d/%s", base, id, suffix)
}

func TestATaskGoesFromSentinelctlToTheAgentAndBack(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	cert := p.join(token, "web01")
	agent := p.client(&cert)

	created := p.queue(token, "web01", agentapi.ActionBackupStart, map[string]string{"job": "db"})
	if created.State != agentapi.TaskQueued || created.CreatedBy == "" {
		t.Fatalf("created = %+v", created)
	}
	tasks := p.take(agent, "1s")
	if len(tasks) != 1 || tasks[0].ID != created.ID || tasks[0].Action != agentapi.ActionBackupStart || tasks[0].Params["job"] != "db" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if again := p.take(agent, "0s"); len(again) != 0 {
		t.Errorf("the task was sent twice: %+v", again)
	}
	if status, _ := call(t, agent, "POST", taskRoute(p.agents.URL, created.ID, "log"), "", agentapi.TaskLog{Text: "starting\x1b[2J"}, nil); status != http.StatusNoContent {
		t.Fatalf("log: status %d", status)
	}
	result := agentapi.TaskResult{State: agentapi.TaskSucceeded, StartedAt: time.Now(), FinishedAt: time.Now(), Output: "started, pid 7"}
	if status, _ := call(t, agent, "POST", taskRoute(p.agents.URL, created.ID, "result"), "", result, nil); status != http.StatusNoContent {
		t.Fatalf("result: status %d", status)
	}
	if status, _ := call(t, agent, "POST", taskRoute(p.agents.URL, created.ID, "result"), "", result, nil); status != http.StatusConflict {
		t.Errorf("a second result: status %d", status)
	}

	var detail ctlapi.TaskDetail
	if status, _ := call(t, p.client(nil), "GET", fmt.Sprintf("%s/v1/ctl/tasks/%d", p.ctl.URL, created.ID), token, nil, &detail); status != http.StatusOK {
		t.Fatalf("show: status %d", status)
	}
	if detail.State != agentapi.TaskSucceeded || detail.Output != "started, pid 7" || len(detail.Log) != 1 || detail.Log[0].Text != "starting" {
		t.Errorf("detail = %+v", detail)
	}
	var listed []ctlapi.Task
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteTasks+"?agent=web01", token, nil, &listed); status != http.StatusOK || len(listed) != 1 {
		t.Errorf("list: status %d, %d tasks", status, len(listed))
	}
	var audited []ctlapi.AuditRecord
	call(t, p.client(nil), "GET", p.ctl.URL+ctlapi.RouteAudit+"?limit=20", token, nil, &audited)
	var actors []string
	for _, record := range audited {
		if strings.HasPrefix(record.Action, "task.") {
			actors = append(actors, record.Action+"/"+record.Actor)
		}
	}
	if got := strings.Join(actors, " "); got != "task.result/agent:web01 task.send/agent:web01 task.create/ctl" {
		t.Errorf("audit of the task = %q", got)
	}
}

func TestSentinelctlCannotQueueWhatTheTableDoesNotHave(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	p.join(token, "web01")
	if status, _ := call(t, p.client(nil), "POST", p.ctl.URL+"/v1/ctl/agents", token, ctlapi.AddAgentRequest{Name: "new01"}, nil); status != http.StatusOK {
		t.Fatalf("add: status %d", status)
	}
	for name, req := range map[string]ctlapi.CreateTaskRequest{
		"a shell":             {Agent: "web01", Action: "shell.run", Params: map[string]string{"cmd": "id"}},
		"a missing parameter": {Agent: "web01", Action: agentapi.ActionBackupStart},
		"a path":              {Agent: "web01", Action: agentapi.ActionBackupStart, Params: map[string]string{"job": "x", "path": "/etc/shadow"}},
		"a line break":        {Agent: "web01", Action: agentapi.ActionBackupStart, Params: map[string]string{"job": "x\ny"}},
	} {
		if status, _ := call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteTasks, token, req, nil); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, status)
		}
	}
	for name, agent := range map[string]string{"a server that has not joined": "new01", "one that does not exist": "ghost"} {
		if status, _ := call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteTasks, token, ctlapi.CreateTaskRequest{Agent: agent, Action: agentapi.ActionDoctorRun}, nil); status != http.StatusConflict {
			t.Errorf("%s: status %d, want 409", name, status)
		}
	}
	if status, _ := call(t, p.client(nil), "POST", p.ctl.URL+ctlapi.RouteTasks, token, json.RawMessage(`{"agent":"web01","action":"doctor.run","extra":1}`), nil); status != http.StatusBadRequest {
		t.Errorf("an unknown field: status %d", status)
	}
}

func TestAnAgentCanOnlyAnswerItsOwnTaskWithAnEndState(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	web, db := p.join(token, "web01"), p.join(token, "db01")
	created := p.queue(token, "web01", agentapi.ActionDoctorRun, nil)
	p.take(p.client(&web), "1s")

	other := p.client(&db)
	good := agentapi.TaskResult{State: agentapi.TaskSucceeded}
	if status, _ := call(t, other, "POST", taskRoute(p.agents.URL, created.ID, "result"), "", good, nil); status != http.StatusNotFound {
		t.Errorf("another agent's result: status %d", status)
	}
	if status, _ := call(t, other, "POST", taskRoute(p.agents.URL, created.ID, "log"), "", agentapi.TaskLog{Text: "x"}, nil); status != http.StatusNotFound {
		t.Errorf("another agent's log: status %d", status)
	}
	if status, _ := call(t, p.client(&web), "POST", taskRoute(p.agents.URL, created.ID, "result"), "", agentapi.TaskResult{State: agentapi.TaskCanceled}, nil); status != http.StatusBadRequest {
		t.Errorf("a result that says canceled: status %d", status)
	}
	if status, _ := call(t, p.client(&web), "POST", taskRoute(p.agents.URL, 999, "result"), "", good, nil); status != http.StatusNotFound {
		t.Errorf("a task that does not exist: status %d", status)
	}
	if status, _ := call(t, p.client(&web), "POST", taskRoute(p.agents.URL, 0, "result"), "", good, nil); status != http.StatusBadRequest {
		t.Errorf("task 0: status %d", status)
	}
	if status, _ := call(t, p.client(&web), "GET", p.agents.URL+agentapi.RouteTasks+"?wait=soon", "", nil, nil); status != http.StatusBadRequest {
		t.Errorf("a wait that is not a duration: status %d", status)
	}
}

func TestAPollIsAnsweredAsSoonAsATaskIsQueued(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	cert := p.join(token, "web01")
	started := time.Now()
	answered := make(chan []agentapi.Task, 1)
	go func() { answered <- p.take(p.client(&cert), "20s") }()
	time.Sleep(300 * time.Millisecond)
	p.queue(token, "web01", agentapi.ActionDoctorRun, nil)
	select {
	case tasks := <-answered:
		if len(tasks) != 1 || time.Since(started) > 5*time.Second {
			t.Errorf("%d tasks after %s", len(tasks), time.Since(started))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the poll did not hear of the task")
	}
}

func TestCancelTakesBackAWaitingTaskOnly(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	cert := p.join(token, "web01")
	waiting := p.queue(token, "web01", agentapi.ActionDoctorRun, nil)
	if status, _ := call(t, p.client(nil), "DELETE", fmt.Sprintf("%s/v1/ctl/tasks/%d", p.ctl.URL, waiting.ID), token, nil, nil); status != http.StatusNoContent {
		t.Errorf("cancel: status %d", status)
	}
	if status, _ := call(t, p.client(nil), "DELETE", fmt.Sprintf("%s/v1/ctl/tasks/%d", p.ctl.URL, waiting.ID), token, nil, nil); status != http.StatusConflict {
		t.Errorf("cancel twice: status %d", status)
	}
	sent := p.queue(token, "web01", agentapi.ActionDoctorRun, nil)
	p.take(p.client(&cert), "1s")
	if status, _ := call(t, p.client(nil), "DELETE", fmt.Sprintf("%s/v1/ctl/tasks/%d", p.ctl.URL, sent.ID), token, nil, nil); status != http.StatusConflict {
		t.Errorf("cancel a task that was sent: status %d", status)
	}
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+"/v1/ctl/tasks/abc", token, nil, nil); status != http.StatusBadRequest {
		t.Errorf("a task number that is not a number: status %d", status)
	}
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+"/v1/ctl/tasks/999", token, nil, nil); status != http.StatusNotFound {
		t.Errorf("a task that does not exist: status %d", status)
	}
}

func TestTheTaskRoutesNeedTheirCredentials(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	p.join(token, "web01")
	for _, route := range []string{ctlapi.RouteTasks, "/v1/ctl/tasks/1", "/v1/ctl/tasks/1/events"} {
		if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+route, "", nil, nil); status != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: status %d", route, status)
		}
	}
	for _, route := range []string{agentapi.RouteTasks, "/v1/agent/tasks/1/result", "/v1/agent/tasks/1/log"} {
		method := "POST"
		if route == agentapi.RouteTasks {
			method = "GET"
		}
		if status, _ := call(t, p.client(nil), method, p.agents.URL+route, "", nil, nil); status != http.StatusUnauthorized {
			t.Errorf("%s %s without a certificate: status %d", method, route, status)
		}
	}
}

func TestTheEventsOfAFinishedTaskAreItsStateItsLogAndDone(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	cert := p.join(token, "web01")
	agent := p.client(&cert)
	created := p.queue(token, "web01", agentapi.ActionDoctorRun, nil)
	p.take(agent, "1s")
	for _, text := range []string{"one", "two\nlines", "three"} {
		call(t, agent, "POST", taskRoute(p.agents.URL, created.ID, "log"), "", agentapi.TaskLog{Text: text}, nil)
	}
	call(t, agent, "POST", taskRoute(p.agents.URL, created.ID, "result"), "", agentapi.TaskResult{State: agentapi.TaskFailed, Error: "boom", Output: "out"}, nil)

	route := fmt.Sprintf("/v1/ctl/tasks/%d/events", created.ID)
	events := readEvents(t, p, token, route, "")
	var names []string
	for _, e := range events {
		names = append(names, e.name)
	}
	if got := strings.Join(names, " "); got != "state log log log log done" {
		t.Fatalf("events = %q", got)
	}
	var line ctlapi.TaskLine
	if err := json.Unmarshal([]byte(events[2].data), &line); err != nil || line.Text != "two" || events[2].id != "2" {
		t.Errorf("second line = %+v (id %q), err %v: a piece with a line break is one line each", line, events[2].id, err)
	}
	var done ctlapi.Task
	if err := json.Unmarshal([]byte(events[5].data), &done); err != nil || done.State != agentapi.TaskFailed || done.Error != "boom" || done.Output != "out" {
		t.Errorf("done = %+v, err %v", done, err)
	}

	resumed := readEvents(t, p, token, route, "3")
	names = nil
	for _, e := range resumed {
		names = append(names, e.name+":"+e.id)
	}
	if got := strings.Join(names, " "); got != "state: log:4 done:" {
		t.Errorf("after Last-Event-ID 3 the events are %q", got)
	}
	if status, _ := call(t, p.client(nil), "GET", p.ctl.URL+"/v1/ctl/tasks/999/events", token, nil, nil); status != http.StatusNotFound {
		t.Errorf("events of a task that does not exist: status %d", status)
	}
}

func TestTheEventsOfARunningTaskFollowItToTheEnd(t *testing.T) {
	p := newPlant(t)
	token := p.signedIn()
	cert := p.join(token, "web01")
	agent := p.client(&cert)
	created := p.queue(token, "web01", agentapi.ActionDoctorRun, nil)
	p.take(agent, "1s")

	got := make(chan []event, 1)
	go func() { got <- readEvents(t, p, token, fmt.Sprintf("/v1/ctl/tasks/%d/events", created.ID), "") }()
	time.Sleep(1500 * time.Millisecond)
	call(t, agent, "POST", taskRoute(p.agents.URL, created.ID, "log"), "", agentapi.TaskLog{Text: "halfway"}, nil)
	time.Sleep(1500 * time.Millisecond)
	call(t, agent, "POST", taskRoute(p.agents.URL, created.ID, "result"), "", agentapi.TaskResult{State: agentapi.TaskSucceeded}, nil)
	select {
	case events := <-got:
		var names []string
		for _, e := range events {
			names = append(names, e.name)
		}
		if strings.Join(names, " ") != "state log state done" {
			t.Errorf("events = %v", names)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end when the task did")
	}
}
