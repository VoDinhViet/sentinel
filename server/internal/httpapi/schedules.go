package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"sentinel/pkg/termtext"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/scheduler"
)

// scheduleError answers a failure of the scheduler.
func scheduleError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, scheduler.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, scheduler.ErrInvalid), errors.Is(err, scheduler.ErrNoAgent):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, scheduler.ErrTooMany):
		writeError(w, http.StatusConflict, err.Error())
	default:
		internalError(w, r, err)
	}
}

func pathNumber(w http.ResponseWriter, r *http.Request, what string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "the number of "+what+" is a positive number")
		return 0, false
	}
	return id, true
}

func ctlSchedule(one scheduler.Schedule, now time.Time) ctlapi.Schedule {
	params := make(map[string]string, len(one.Params))
	for name, value := range one.Params {
		params[termtext.Line(name)] = termtext.Line(value)
	}
	listed := ctlapi.Schedule{
		ID: one.ID, Agent: one.Agent, Action: one.Action, Params: params, Spec: one.Spec, CreatedAt: one.CreatedAt,
		CreatedBy: termtext.Line(one.CreatedBy), LastRunAt: one.LastRunAt, LastTask: one.LastTask, LastError: termtext.Line(one.LastError),
	}
	if next, ok := one.Next(now); ok {
		listed.Next = &next
	}
	return listed
}

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request, _ session) {
	found, err := s.app.Scheduler.Schedules(r.Context(), r.URL.Query().Get("agent"))
	if err != nil {
		internalError(w, r, err)
		return
	}
	now := time.Now()
	listed := make([]ctlapi.Schedule, 0, len(found))
	for _, one := range found {
		listed = append(listed, ctlSchedule(one, now))
	}
	writeJSON(w, http.StatusOK, listed)
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request, _ session) {
	var req ctlapi.CreateScheduleRequest
	if !readJSON(w, r, maxCtlBody, &req) {
		return
	}
	made, err := s.app.Scheduler.AddSchedule(r.Context(), req.Agent, req.Spec, req.Action, req.Params)
	if err != nil {
		scheduleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ctlSchedule(made, time.Now()))
}

func (s *Server) removeSchedule(w http.ResponseWriter, r *http.Request, _ session) {
	id, ok := pathNumber(w, r, "a schedule")
	if !ok {
		return
	}
	if err := s.app.Scheduler.RemoveSchedule(r.Context(), id); err != nil {
		scheduleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func ctlWindow(one scheduler.Window, now time.Time) ctlapi.Window {
	return ctlapi.Window{
		ID: one.ID, Agent: one.Agent, StartsAt: one.StartsAt, EndsAt: one.EndsAt, Reason: termtext.Line(one.Reason),
		Open: !now.Before(one.StartsAt) && now.Before(one.EndsAt), CreatedBy: termtext.Line(one.CreatedBy),
	}
}

func (s *Server) listWindows(w http.ResponseWriter, r *http.Request, _ session) {
	now := time.Now()
	found, err := s.app.Scheduler.Windows(r.Context(), now)
	if err != nil {
		internalError(w, r, err)
		return
	}
	listed := make([]ctlapi.Window, 0, len(found))
	for _, one := range found {
		listed = append(listed, ctlWindow(one, now))
	}
	writeJSON(w, http.StatusOK, listed)
}

func (s *Server) createWindow(w http.ResponseWriter, r *http.Request, _ session) {
	var req ctlapi.CreateWindowRequest
	if !readJSON(w, r, maxCtlBody, &req) {
		return
	}
	if req.All == (req.Agent != "") {
		writeError(w, http.StatusBadRequest, "a window is for one server (agent) or for all of them (all: true), not both and not neither")
		return
	}
	now := time.Now()
	start := now
	if req.StartsAt != nil {
		start = *req.StartsAt
	}
	made, err := s.app.Scheduler.AddWindow(r.Context(), req.Agent, start, req.EndsAt, req.Reason)
	if err != nil {
		scheduleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ctlWindow(made, now))
}

func (s *Server) removeWindow(w http.ResponseWriter, r *http.Request, _ session) {
	id, ok := pathNumber(w, r, "a maintenance window")
	if !ok {
		return
	}
	if err := s.app.Scheduler.RemoveWindow(r.Context(), id); err != nil {
		scheduleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
