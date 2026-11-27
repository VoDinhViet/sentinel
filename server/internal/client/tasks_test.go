package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"sentinel/server/internal/ctlapi"
)

// streamOf is a handler that writes the given events, in order, then closes. onConnect says which
// connection it is (0, 1, …) and what Last-Event-ID it sent.
func streamOf(connections *atomic.Int32, lastIDs *[]string, body ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connection := int(connections.Add(1)) - 1
		*lastIDs = append(*lastIDs, r.Header.Get("Last-Event-ID"))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body[connection])
	}
}

func TestFollowTaskGivesEventsInOrderAndReturnsTheEndedTask(t *testing.T) {
	var connections atomic.Int32
	var lastIDs []string
	server, cfg := serve(t, streamOf(&connections, &lastIDs,
		"event: state\ndata: {\"state\":\"sent\"}\n\n"+
			"event: log\nid: 1\ndata: {\"seq\":1,\"text\":\"one\\u001b[2J\"}\n\n"+
			": keep-alive\n\n"+
			"event: future\ndata: {}\n\n"+
			"event: done\ndata: {\"id\":4,\"state\":\"failed\",\"error\":\"boom\\nx\",\"output\":\"out\\u001b[31m\"}\n\n"))
	_ = server
	api, err := New(cfg, "tok")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var lines []string
	ended, err := api.FollowTask(context.Background(), 4, func(event TaskEvent) error {
		kinds = append(kinds, event.Kind)
		if event.Kind == ctlapi.EventLog {
			lines = append(lines, event.Line.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(kinds, " "); got != "state log unknown done" {
		t.Errorf("kinds = %q", got)
	}
	if len(lines) != 1 || lines[0] != "one" {
		t.Errorf("lines = %q: the escape sequence must be dropped", lines)
	}
	if ended.State != "failed" || ended.Error != "boom x" || ended.Output != "out" {
		t.Errorf("ended = %+v", ended)
	}
}

func TestFollowTaskResumesFromTheLastLineWhenTheConnectionIsCut(t *testing.T) {
	var connections atomic.Int32
	var lastIDs []string
	server, cfg := serve(t, streamOf(&connections, &lastIDs,
		"event: log\nid: 1\ndata: {\"seq\":1,\"text\":\"a\"}\n\nevent: log\nid: 2\ndata: {\"seq\":2,\"text\":\"b\"}\n\n",
		"event: log\nid: 3\ndata: {\"seq\":3,\"text\":\"c\"}\n\nevent: done\ndata: {\"id\":1,\"state\":\"succeeded\"}\n\n"))
	_ = server
	api, _ := New(cfg, "tok")
	var texts []string
	ended, err := api.FollowTask(context.Background(), 1, func(event TaskEvent) error {
		if event.Kind == ctlapi.EventLog {
			texts = append(texts, event.Line.Text)
		}
		return nil
	})
	if err != nil || ended.State != "succeeded" {
		t.Fatalf("ended %+v, err %v", ended, err)
	}
	if strings.Join(texts, "") != "abc" || strings.Join(lastIDs, ",") != ",2" {
		t.Errorf("texts %v, Last-Event-ID of the connections %q: the second must resume after line 2", texts, lastIDs)
	}
}

func TestFollowTaskGivesUpAfterAFewCutsAndAnswers4xxAtOnce(t *testing.T) {
	var connections atomic.Int32
	server, cfg := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		w.Header().Set("Content-Type", "text/event-stream") // and nothing more: a cut
	}))
	_ = server
	api, _ := New(cfg, "tok")
	if testing.Short() {
		t.Skip("waits a second per cut")
	}
	if _, err := api.FollowTask(context.Background(), 1, func(TaskEvent) error { return nil }); err == nil || !strings.Contains(err.Error(), "cut too many times") {
		t.Errorf("err = %v", err)
	}
	if got := connections.Load(); got != followRetries+1 {
		t.Errorf("connections = %d, want %d", got, followRetries+1)
	}

	var asked atomic.Int32
	notFound, cfg2 := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"no such task"}`)
	}))
	_ = notFound
	api2, _ := New(cfg2, "tok")
	_, err := api2.FollowTask(context.Background(), 9, func(TaskEvent) error { return nil })
	var refused *Error
	if !errors.As(err, &refused) || refused.Status != 404 || asked.Load() != 1 {
		t.Errorf("err = %v after %d asks", err, asked.Load())
	}
}

func TestFollowTaskStopsWhenTheCallerSaysSoAndOnAnEndedSession(t *testing.T) {
	var connections atomic.Int32
	var lastIDs []string
	server, cfg := serve(t, streamOf(&connections, &lastIDs, "event: state\ndata: {\"state\":\"sent\"}\n\n"))
	_ = server
	api, _ := New(cfg, "tok")
	enough := errors.New("enough")
	if _, err := api.FollowTask(context.Background(), 1, func(TaskEvent) error { return enough }); !errors.Is(err, enough) || connections.Load() != 1 {
		t.Errorf("err = %v after %d connections", err, connections.Load())
	}

	ended, cfg2 := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	_ = ended
	api2, _ := New(cfg2, "tok")
	if _, err := api2.FollowTask(context.Background(), 1, func(TaskEvent) error { return nil }); !errors.Is(err, ErrSessionEnded) {
		t.Errorf("err = %v", err)
	}
}

func TestTasksAreCleanedAndAskedForOneServer(t *testing.T) {
	var query atomic.Value
	server, cfg := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.RawQuery)
		fmt.Fprint(w, `[{"id":1,"agent":"web\u001b[31m01","action":"backup.start","params":{"job":"a\u001b[2Jb"},"state":"failed","error":"x\ny","output":"o\u001b[31m"}]`)
	}))
	_ = server
	api, _ := New(cfg, "tok")
	tasks, err := api.Tasks(context.Background(), "web01", 5)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks %v, err %v", tasks, err)
	}
	if tasks[0].Agent != "web01" || tasks[0].Params["job"] != "ab" || tasks[0].Error != "x y" || tasks[0].Output != "o" {
		t.Errorf("task = %+v", tasks[0])
	}
	if query.Load() != "agent=web01&limit=5" {
		t.Errorf("query = %v", query.Load())
	}
}
