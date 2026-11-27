package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/terminal"
)

// activeServer adds a server and makes it active, as a join would.
func (p *plant) activeServer(name string) {
	p.t.Helper()
	if _, err := p.run("", "server", "add", name); err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.app.Store.DB.Exec(`UPDATE agents SET state = 'active' WHERE name = ?`, name); err != nil {
		p.t.Fatal(err)
	}
}

// answerOne plays an agent: it takes the next task of the server, writes a log line and sends the result.
func (p *plant) answerOne(name string, result agentapi.TaskResult) {
	p.t.Helper()
	ctx := audit.WithActor(context.Background(), audit.Actor{Name: "agent:" + name})
	tasks, err := p.app.Tasks.Poll(ctx, name, 10*time.Second)
	if err != nil || len(tasks) != 1 {
		p.t.Errorf("the agent got %v, %v", tasks, err)
		return
	}
	_ = p.app.Tasks.AppendLog(ctx, name, tasks[0].ID, "working on it")
	time.Sleep(1200 * time.Millisecond) // the stream looks at the database once a second
	if err := p.app.Tasks.Result(ctx, name, tasks[0].ID, result); err != nil {
		p.t.Errorf("result: %v", err)
	}
}

func TestRunQueuesATaskForAServerThatHasJoined(t *testing.T) {
	p := newPlant(t)
	p.login()
	if _, err := p.run("", "server", "add", "new01"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.run("", "run", "new01", "doctor.run"); err == nil || !strings.Contains(err.Error(), "has not joined") {
		t.Errorf("a server that has not joined: %v", err)
	}
	p.activeServer("web01")
	out, err := p.run("", "run", "web01", "backup.start", "job=db")
	if err != nil || !strings.Contains(out, "Đã xếp việc #1 (backup.start) cho web01") {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for name, args := range map[string][]string{
		"an action that is not in the table": {"run", "web01", "shell.run", "cmd=id"},
		"no parameter name":                  {"run", "web01", "backup.start", "=db"},
		"a parameter without a value":        {"run", "web01", "backup.start", "job"},
		"a parameter twice":                  {"run", "web01", "backup.start", "job=a", "job=b"},
		"a missing parameter":                {"run", "web01", "backup.start"},
		"a parameter the action lacks":       {"run", "web01", "doctor.run", "path=/etc"},
	} {
		if _, err := p.run("", args...); err == nil {
			t.Errorf("%s was queued", name)
		}
	}
	if out, _ := p.run("", "task", "ls"); !strings.Contains(out, "#1") || !strings.Contains(out, "job=db") || !strings.Contains(out, "queued") {
		t.Errorf("task ls:\n%s", out)
	}
	if out, err := p.run("", "task", "cancel", "1"); err != nil || !strings.Contains(out, "Đã rút việc #1") {
		t.Errorf("cancel: %v\n%s", err, out)
	}
	if _, err := p.run("", "task", "cancel", "#1"); err == nil || !strings.Contains(err.Error(), "không còn chờ") {
		t.Errorf("cancel twice: %v", err)
	}
	if _, err := p.run("", "task", "show", "abc"); err == nil {
		t.Error("task show abc worked")
	}
}

func TestRunFollowPrintsTheLogAndTheResultAndFailsWhenTheTaskDoes(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.answerOne("web01", agentapi.TaskResult{State: agentapi.TaskSucceeded, Output: "all fine\nsecond line"})
	}()
	out, err := p.run("", "run", "web01", "doctor.run", "--follow")
	<-done
	if err != nil {
		t.Fatalf("run --follow: %v\n%s", err, out)
	}
	for _, want := range []string{"working on it", "Việc #1: succeeded", "all fine", "second line"} {
		if !strings.Contains(out, want) {
			t.Errorf("--follow output lacks %q:\n%s", want, out)
		}
	}

	done = make(chan struct{})
	go func() {
		defer close(done)
		p.answerOne("web01", agentapi.TaskResult{State: agentapi.TaskRefused, Error: "backup is not allowed on this server (control_plane.allow.backup)"})
	}()
	out, err = p.run("", "run", "web01", "backup.start", "job=db", "--wait")
	<-done
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("a refused task: err = %v, want one that says refused (the exit code of scripts)", err)
	}
	if !strings.Contains(out, "not allowed") || strings.Contains(out, "working on it") {
		t.Errorf("--wait prints the error and not the log:\n%s", out)
	}
}

func TestRunWithJSONGivesTheEndedTask(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.answerOne("web01", agentapi.TaskResult{State: agentapi.TaskSucceeded, Output: "ok"})
	}()
	out, err := p.run("", "run", "web01", "doctor.run", "--wait", "-o", "json")
	<-done
	var ended ctlapi.Task
	if err != nil || json.Unmarshal([]byte(out), &ended) != nil || ended.State != agentapi.TaskSucceeded || ended.Output != "ok" {
		t.Errorf("run --wait -o json: %v\n%s", err, out)
	}
}

func TestTaskShowPrintsTheStepsTheLogAndTheOutput(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.answerOne("web01", agentapi.TaskResult{State: agentapi.TaskFailed, Error: "restic: repository is locked", Output: "partial"})
	}()
	_, _ = p.run("", "run", "web01", "backup.start", "job=db", "--wait")
	<-done
	out, err := p.run("", "task", "show", "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Việc #1", "backup.start", "job=db", "failed", "Server:    web01", "Xếp lúc:", "Gửi lúc:", "Xong lúc:", "Lỗi:       restic: repository is locked", "working on it", "partial"} {
		if !strings.Contains(out, want) {
			t.Errorf("task show lacks %q:\n%s", want, out)
		}
	}
	if out, _ := p.run("", "task", "ls", "--server", "other"); !strings.Contains(out, "Chưa có việc nào") {
		t.Errorf("task ls --server other:\n%s", out)
	}
}

func TestServerShowSaysWhichActionsTheServerAllows(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	save := func(allow []string) string {
		report := agentapi.Report{Version: "1", Allow: allow}
		if err := p.app.Inventory.SaveReport(context.Background(), "web01", report, time.Now()); err != nil {
			t.Fatal(err)
		}
		out, _ := p.run("", "server", "show", "web01")
		return out
	}
	if out := save(nil); !strings.Contains(out, "không có action nào") {
		t.Errorf("no allowlist:\n%s", out)
	}
	if out := save([]string{"doctor.run", "backup.start"}); !strings.Contains(out, "Cho phép:          doctor.run, backup.start") {
		t.Errorf("an allowlist:\n%s", out)
	}
}

func TestAnActionThatIsHardToTakeBackAsksForTheNameOfTheServer(t *testing.T) {
	p := newPlant(t)
	p.login()
	p.activeServer("web01")
	typing := func(text string) terminal.Terminal {
		return terminal.Terminal{In: strings.NewReader(text), Out: io.Discard, IsTTY: true}
	}
	queued := func() int {
		var count int
		if err := p.app.Store.DB.QueryRow(`SELECT COUNT(*) FROM tasks WHERE action = ?`, agentapi.ActionOSApplyUpdates).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if _, err := p.run("web01\n", "run", "web01", agentapi.ActionOSApplyUpdates); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("a script without a terminal: %v", err)
	}
	if _, err := p.runIn(typing("web02\n"), "run", "web01", agentapi.ActionOSApplyUpdates); err == nil || !strings.Contains(err.Error(), "không khớp") {
		t.Errorf("the wrong name: %v", err)
	}
	if _, err := p.runIn(typing(""), "run", "web01", agentapi.ActionOSApplyUpdates); err == nil {
		t.Error("no answer was taken as a yes")
	}
	if queued() != 0 {
		t.Fatal("a task was queued without a confirmation")
	}
	if _, err := p.runIn(typing("web01\n"), "run", "web01", agentapi.ActionOSApplyUpdates); err != nil || queued() != 1 {
		t.Errorf("the right name: %v, %d queued", err, queued())
	}
	if _, err := p.run("", "run", "web01", agentapi.ActionOSApplyUpdates, "--yes"); err != nil || queued() != 2 {
		t.Errorf("--yes: %v, %d queued", err, queued())
	}
	// An action that is easy to take back does not ask.
	if _, err := p.run("", "run", "web01", agentapi.ActionOSCheckUpdates); err != nil {
		t.Errorf("os.check_updates asked for a confirmation: %v", err)
	}
}
