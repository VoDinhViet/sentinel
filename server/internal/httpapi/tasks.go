package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/pkg/termtext"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/task"
)

// The limits of the task routes of the agent: a result may carry 64K characters of output, and a
// character can take six bytes in JSON (\u001b).
const (
	maxTaskResultBody = 512 << 10
	maxTaskLogBody    = 128 << 10
	// maxPollWait is the longest an agent may ask to wait: under the write timeout of the server.
	maxPollWait = 25 * time.Second
)

// agentActor says in the audit log that an agent does what the handler does.
func agentActor(r *http.Request, agent caller) *http.Request {
	return r.WithContext(audit.WithActor(r.Context(), audit.Actor{Name: "agent:" + agent.Name, IP: clientIP(r)}))
}

// pollTasks gives the agent the tasks waiting for it, or waits up to ?wait= (25 s at most) for one.
func (s *Server) pollTasks(w http.ResponseWriter, r *http.Request, agent caller) {
	wait := maxPollWait
	if text := r.URL.Query().Get("wait"); text != "" {
		asked, err := time.ParseDuration(text)
		if err != nil || asked < 0 {
			writeError(w, http.StatusBadRequest, "wait is a duration such as 25s")
			return
		}
		wait = min(asked, maxPollWait)
	}
	r = agentActor(r, agent)
	tasks, err := s.app.Tasks.Poll(r.Context(), agent.Name, wait)
	if err != nil {
		internalError(w, r, err)
		return
	}
	if tasks == nil {
		tasks = []agentapi.Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func taskID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "the task number is a positive number")
		return 0, false
	}
	return id, true
}

// taskError answers a failure of a call of the queue.
func taskError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, task.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, task.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, task.ErrAgentNotReady):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, task.ErrTooMany), errors.Is(err, task.ErrNotQueued), errors.Is(err, task.ErrNotRunning):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, task.ErrLogFull):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
	default:
		internalError(w, r, err)
	}
}

// taskResult records what the agent says of a task.
func (s *Server) taskResult(w http.ResponseWriter, r *http.Request, agent caller) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var result agentapi.TaskResult
	if !readJSONLoose(w, r, maxTaskResultBody, &result) {
		return
	}
	r = agentActor(r, agent)
	if err := s.app.Tasks.Result(r.Context(), agent.Name, id, result); err != nil {
		taskError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// taskLog adds a piece to the log of a task of the agent.
func (s *Server) taskLog(w http.ResponseWriter, r *http.Request, agent caller) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	var chunk agentapi.TaskLog
	if !readJSONLoose(w, r, maxTaskLogBody, &chunk) {
		return
	}
	if err := s.app.Tasks.AppendLog(agentActor(r, agent).Context(), agent.Name, id, chunk.Text); err != nil {
		taskError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ctlTask is a task as sentinelctl sees it. What an agent wrote is cleaned again, whatever was stored.
func ctlTask(found task.Task) ctlapi.Task {
	return ctlapi.Task{
		ID: found.ID, Agent: found.Agent, Action: found.Action, Params: cleanParams(found.Params), State: found.State,
		CreatedAt: found.CreatedAt, CreatedBy: termtext.Line(found.CreatedBy), SentAt: found.SentAt, StartedAt: found.StartedAt, FinishedAt: found.FinishedAt,
		Output: agentapi.CleanText(found.Output, agentapi.MaxTaskOutput), Error: termtext.Line(found.Error),
	}
}

func cleanParams(params map[string]string) map[string]string {
	cleaned := make(map[string]string, len(params))
	for name, value := range params {
		cleaned[termtext.Line(name)] = termtext.Line(value)
	}
	return cleaned
}

func ctlLine(line task.Line) ctlapi.TaskLine {
	return ctlapi.TaskLine{Seq: line.Seq, At: line.At, Text: agentapi.CleanText(line.Text, agentapi.MaxTaskLogChunk)}
}

// createTask queues an action for a server.
func (s *Server) createTask(w http.ResponseWriter, r *http.Request, _ session) {
	var req ctlapi.CreateTaskRequest
	if !readJSON(w, r, maxCtlBody, &req) {
		return
	}
	created, err := s.app.Tasks.Create(r.Context(), req.Agent, req.Action, req.Params)
	if err != nil {
		taskError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ctlTask(created))
}

const (
	defaultTaskLimit = 20
	maxTaskLimit     = 200
)

// listTasks answers the newest tasks, of all servers or of the one in ?agent=.
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, _ session) {
	limit := defaultTaskLimit
	if text := r.URL.Query().Get("limit"); text != "" {
		number, err := strconv.Atoi(text)
		if err != nil || number < 1 || number > maxTaskLimit {
			writeError(w, http.StatusBadRequest, "limit must be a number from 1 to "+strconv.Itoa(maxTaskLimit))
			return
		}
		limit = number
	}
	found, err := s.app.Tasks.List(r.Context(), r.URL.Query().Get("agent"), limit)
	if err != nil {
		internalError(w, r, err)
		return
	}
	listed := make([]ctlapi.Task, 0, len(found))
	for _, one := range found {
		listed = append(listed, ctlTask(one))
	}
	writeJSON(w, http.StatusOK, listed)
}

// showTask answers one task with its log.
func (s *Server) showTask(w http.ResponseWriter, r *http.Request, _ session) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	found, err := s.app.Tasks.Get(r.Context(), id)
	if err != nil {
		taskError(w, r, err)
		return
	}
	lines, err := s.app.Tasks.Log(r.Context(), id, 0)
	if err != nil {
		internalError(w, r, err)
		return
	}
	detail := ctlapi.TaskDetail{Task: ctlTask(found), Log: make([]ctlapi.TaskLine, 0, len(lines))}
	for _, line := range lines {
		detail.Log = append(detail.Log, ctlLine(line))
	}
	writeJSON(w, http.StatusOK, detail)
}

// cancelTask takes back a task that no agent has taken yet.
func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request, _ session) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	if err := s.app.Tasks.Cancel(r.Context(), id); err != nil {
		taskError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The pace of the stream of a task: how often the database is looked at, how often a comment keeps an
// idle connection alive, how long one stream may last (every task ends sooner: it expires), and how long
// a write may take before the client is taken for gone.
const (
	eventsEvery     = time.Second
	eventsKeepAlive = 15 * time.Second
	eventsMaxTime   = 20 * time.Minute
	eventsWriteWait = 15 * time.Second
	// eventsReauth is how often a stream asks again whether its session is still there, so that a password
	// change (which ends every session) ends the streams too; maxStreams is how many may be open at once.
	eventsReauth = time.Minute
	maxStreams   = 10
)

// taskEvents streams a task as server-sent events: its state when it changes, each line of its log (the
// id of the event is the number of the line, so Last-Event-ID resumes), and "done" with the task as it ended.
func (s *Server) taskEvents(w http.ResponseWriter, r *http.Request, who session) {
	id, ok := taskID(w, r)
	if !ok {
		return
	}
	if s.streams.Add(1) > maxStreams {
		s.streams.Add(-1)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "too many task streams are open: close one, or wait")
		return
	}
	defer s.streams.Add(-1)
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if _, err := s.app.Tasks.Get(r.Context(), id); err != nil {
		taskError(w, r, err)
		return
	}
	stream := &eventStream{w: w, control: http.NewResponseController(w)}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	deadline := time.NewTimer(eventsMaxTime)
	defer deadline.Stop()
	tick := time.NewTicker(eventsEvery)
	defer tick.Stop()
	lastWrite, lastState, lastAuth := time.Now(), "", time.Now()
	for {
		if time.Since(lastAuth) >= eventsReauth {
			if _, err := s.app.Auth.Authenticate(r.Context(), who.Token); err != nil {
				return // the session ended (or the store failed): the client asks again and is told
			}
			lastAuth = time.Now()
		}
		// The task is read before its log: a task seen finished has all its log, so no line is missed.
		current, err := s.app.Tasks.Get(r.Context(), id)
		if err != nil {
			return
		}
		lines, err := s.app.Tasks.Log(r.Context(), id, after)
		if err != nil {
			return
		}
		if current.State != lastState {
			lastState = current.State
			if !stream.send("state", "", map[string]string{"state": current.State}) {
				return
			}
			lastWrite = time.Now()
		}
		for _, line := range lines {
			after = line.Seq
			if !stream.send(ctlapi.EventLog, strconv.FormatInt(line.Seq, 10), ctlLine(line)) {
				return
			}
			lastWrite = time.Now()
		}
		if agentapi.Finished(current.State) {
			stream.send(ctlapi.EventDone, "", ctlTask(current))
			return
		}
		if time.Since(lastWrite) >= eventsKeepAlive {
			if !stream.comment("keep-alive") {
				return
			}
			lastWrite = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
	}
}

// eventStream writes server-sent events, giving each write its own deadline: the server's own write timeout
// is for an answer, and a stream outlives it.
type eventStream struct {
	w       http.ResponseWriter
	control *http.ResponseController
}

func (e *eventStream) send(event, id string, data any) bool {
	encoded, err := json.Marshal(data) // one line: a line break in the text is \n in JSON
	if err != nil {
		return false
	}
	text := "event: " + event + "\n"
	if id != "" {
		text += "id: " + id + "\n"
	}
	return e.write(text + "data: " + string(encoded) + "\n\n")
}

func (e *eventStream) comment(text string) bool { return e.write(": " + text + "\n\n") }

func (e *eventStream) write(text string) bool {
	_ = e.control.SetWriteDeadline(time.Now().Add(eventsWriteWait))
	if _, err := fmt.Fprint(e.w, text); err != nil {
		return false
	}
	return e.control.Flush() == nil
}
