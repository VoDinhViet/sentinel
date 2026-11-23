package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"sentinel/internal/api"
	"sentinel/internal/server/service"
)

const maxReportBytes = 5 << 20

func (s *Server) routesAgent() {
	s.mux.HandleFunc("POST /api/agent/report", s.withAgent(s.handleReport))
	s.mux.HandleFunc("GET /api/agent/commands", s.withAgent(s.handleCommands))
	s.mux.HandleFunc("POST /api/agent/commands/{cid}/result", s.withAgent(s.handleCommandResult))
	s.mux.HandleFunc("GET /api/agent/config", s.withAgent(s.handleGetConfig))
	s.mux.HandleFunc("PUT /api/agent/config", s.withAgent(s.handleImportConfig))
	s.mux.HandleFunc("POST /api/agent/config/status", s.withAgent(s.handleConfigStatus))
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	var rep api.Report
	if !decodeJSON(w, r, maxReportBytes, &rep) {
		return
	}
	cur, err := s.Service.Ingest(r.Context(), agentFrom(r).ID, rep)
	if err != nil {
		s.serviceError(w, r, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, api.ReportResponse{Cursor: cur})
}

func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	wait, err := time.ParseDuration(r.URL.Query().Get("wait"))
	if err != nil || wait > 30*time.Second {
		wait = 25 * time.Second
	}
	cmds, err := s.Service.WaitCommands(r.Context(), agentFrom(r).ID, wait)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNilCommands(cmds))
}

func nonNilCommands(c []api.Command) []api.Command {
	if c == nil {
		return []api.Command{}
	}
	return c
}

func (s *Server) handleCommandResult(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusNotFound, "no such command")
		return
	}
	var res api.CommandResult
	if !decodeJSON(w, r, 2<<20, &res) {
		return
	}
	err = s.Service.Complete(r.Context(), agentFrom(r).ID, id, res)
	if errors.Is(err, service.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "no running command with that id")
		return
	}
	if err != nil {
		s.serviceError(w, r, err, http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	doc, ok, err := s.Service.CurrentConfig(r.Context(), agentFrom(r).ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		jsonError(w, http.StatusNotFound, "no managed config yet")
		return
	}
	if have, _ := strconv.ParseInt(r.URL.Query().Get("version"), 10, 64); have == doc.Version {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request) {
	var doc api.ConfigDoc
	if !decodeJSON(w, r, 512<<10, &doc) {
		return
	}
	imported, err := s.Service.ImportConfig(r.Context(), agentFrom(r).ID, doc.YAML)
	if err != nil {
		s.serviceError(w, r, err, http.StatusUnprocessableEntity)
		return
	}
	if !imported {
		jsonError(w, http.StatusConflict, "server already has a config for this agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleConfigStatus(w http.ResponseWriter, r *http.Request) {
	var st api.ConfigStatus
	if !decodeJSON(w, r, 64<<10, &st) {
		return
	}
	if err := s.Service.SetConfigStatus(r.Context(), agentFrom(r).ID, st); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
