package server

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"sentinel/internal/api"
	"sentinel/internal/server/service"
)

func (s *Server) routesConfig() {
	s.mux.HandleFunc("GET /agents/{id}/config", s.requireLogin(s.withAgentID(s.handleConfigPage)))
	s.mux.HandleFunc("POST /agents/{id}/config", s.requireLogin(s.withAgentID(s.handleConfigSave)))
	s.mux.HandleFunc("POST /agents/{id}/config/validate", s.requireLogin(s.withAgentID(s.handleConfigValidate)))
	s.mux.HandleFunc("POST /agents/{id}/config/rollback", s.requireLogin(s.withAgentID(s.handleConfigRollback)))
}

func (s *Server) configPage(w http.ResponseWriter, r *http.Request, a service.Agent, data map[string]any) {
	ctx := r.Context()
	cur, ok, err := s.Service.CurrentConfig(ctx, a.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	hist, err := s.Service.ConfigHistory(ctx, a.ID, 50)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	data["Agent"], data["History"] = a, hist
	if ok {
		data["Current"] = cur
	} else {
		data["Current"] = api.ConfigDoc{}
	}
	if _, set := data["YAML"]; !set {
		data["YAML"] = cur.YAML
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("compare"), 10, 64); err == nil && ok {
		old, err := s.Service.ConfigVersion(ctx, a.ID, v)
		if err == nil {
			data["Viewing"], data["Diff"] = old, service.Diff(old.YAML, cur.YAML)
		}
	}
	s.render(w, r, "config.html", data)
}

func (s *Server) handleConfigPage(w http.ResponseWriter, r *http.Request, a service.Agent) {
	saved, _ := strconv.ParseInt(r.URL.Query().Get("saved"), 10, 64)
	s.configPage(w, r, a, map[string]any{"Saved": saved})
}

// normalizeYAML converts browser line endings so diffs stay meaningful.
func normalizeYAML(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

func (s *Server) handleConfigSave(w http.ResponseWriter, r *http.Request, a service.Agent) {
	text := normalizeYAML(r.PostFormValue("yaml"))
	v, err := s.Service.SaveConfig(r.Context(), a.ID, text, strings.TrimSpace(r.PostFormValue("comment")))
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.configPage(w, r, a, map[string]any{"Error": err.Error(), "YAML": text})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/agents/%d/config?saved=%d", a.ID, v), http.StatusSeeOther)
}

func (s *Server) handleConfigValidate(w http.ResponseWriter, r *http.Request, a service.Agent) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := service.ValidateConfig(normalizeYAML(r.PostFormValue("yaml"))); err != nil {
		fmt.Fprintf(w, `<span class="flash bad">%s</span>`, html.EscapeString(err.Error()))
		return
	}
	fmt.Fprint(w, `<span class="badge ok">valid</span> <span class="muted small">secrets (${VAR}) are checked by the agent when it applies the config</span>`)
}

func (s *Server) handleConfigRollback(w http.ResponseWriter, r *http.Request, a service.Agent) {
	version, err := strconv.ParseInt(r.PostFormValue("version"), 10, 64)
	if err != nil {
		http.Error(w, "bad version", http.StatusBadRequest)
		return
	}
	v, err := s.Service.Rollback(r.Context(), a.ID, version)
	if errors.Is(err, service.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.configPage(w, r, a, map[string]any{"Error": err.Error()})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/agents/%d/config?saved=%d", a.ID, v), http.StatusSeeOther)
}
