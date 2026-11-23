package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sentinel/internal/server/service"
)

func (s *Server) routesUsers() {
	s.mux.HandleFunc("GET /api/ui/users", s.ui(service.RoleAdmin, s.handleUsers))
	s.mux.HandleFunc("POST /api/ui/users", s.uiAudited(service.RoleAdmin, "user.create", s.handleUserCreate))
	s.mux.HandleFunc("PATCH /api/ui/users/{uid}", s.uiAudited(service.RoleAdmin, "user.update", s.handleUserUpdate))
	s.mux.HandleFunc("GET /api/ui/audit", s.ui(service.RoleAdmin, s.handleAudit))
}

type userDTO struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
	// Bootstrap marks the admin from server.yaml, which is not editable here.
	Bootstrap bool `json:"bootstrap,omitempty"`
}

func toUserDTO(u service.User) userDTO {
	return userDTO{ID: u.ID, Username: u.Username, Role: u.Role, Disabled: u.Disabled, CreatedAt: u.CreatedAt}
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Service.ListUsers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := []userDTO{{Username: s.Config.Admin.Username, Role: service.RoleAdmin, Bootstrap: true}}
	for _, u := range users {
		out = append(out, toUserDTO(u))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, 4<<10, &in) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	auditNote(r, "username", in.Username)
	auditNote(r, "role", in.Role)
	if strings.EqualFold(in.Username, s.Config.Admin.Username) {
		jsonError(w, http.StatusConflict, "That username is the bootstrap admin from server.yaml.")
		return
	}
	u, err := s.Service.CreateUser(r.Context(), in.Username, in.Password, in.Role)
	if errors.Is(err, service.ErrConflict) {
		jsonError(w, http.StatusConflict, "That username is taken.")
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toUserDTO(u))
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
	}
	if !decodeJSON(w, r, 4<<10, &in) {
		return
	}
	auditNote(r, "user_id", id)
	// Never log the password, only that it was changed.
	if in.Role != nil {
		auditNote(r, "role", *in.Role)
	}
	if in.Disabled != nil {
		auditNote(r, "disabled", *in.Disabled)
	}
	if in.Password != nil {
		auditNote(r, "password_changed", true)
	}
	u, err := s.Service.UpdateUser(r.Context(), id, service.UserUpdate{Role: in.Role, Disabled: in.Disabled, Password: in.Password})
	if errors.Is(err, service.ErrNotFound) {
		jsonError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(u))
}

type auditDTO struct {
	ID        int64          `json:"id"`
	At        time.Time      `json:"at"`
	Username  string         `json:"username"`
	IP        string         `json:"ip"`
	Action    string         `json:"action"`
	AgentID   int64          `json:"agent_id,omitempty"`
	AgentName string         `json:"agent_name,omitempty"`
	Job       string         `json:"job,omitempty"`
	Details   map[string]any `json:"details"`
	Outcome   string         `json:"outcome"`
}

// handleAudit lists the audit log, newest first. Filters: user, action (a
// trailing "." matches a family such as "config."), agent, outcome, before
// (entry id, for paging), limit. With format=csv it downloads a CSV instead.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := service.AuditFilter{Username: q.Get("user"), Action: q.Get("action"), Outcome: q.Get("outcome")}
	f.AgentID, _ = strconv.ParseInt(q.Get("agent"), 10, 64)
	f.BeforeID, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	csvOut := q.Get("format") == "csv"
	if csvOut && f.Limit == 0 {
		f.Limit = 1000
	}
	entries, err := s.Service.ListAudit(r.Context(), f)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if csvOut {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="audit-log.csv"`)
		cw := csv.NewWriter(w)
		cw.Write([]string{"id", "time", "user", "ip", "action", "server", "job", "outcome", "details"})
		for _, e := range entries {
			cw.Write([]string{strconv.FormatInt(e.ID, 10), e.At.UTC().Format(time.RFC3339), csvSafe(e.Username), e.IP, e.Action,
				csvSafe(e.AgentName), csvSafe(e.Job), e.Outcome, csvSafe(fmt.Sprint(e.Details))})
		}
		cw.Flush()
		return
	}
	out := make([]auditDTO, 0, len(entries))
	for _, e := range entries {
		if e.Details == nil {
			e.Details = map[string]any{}
		}
		out = append(out, auditDTO{ID: e.ID, At: e.At, Username: e.Username, IP: e.IP, Action: e.Action,
			AgentID: e.AgentID, AgentName: e.AgentName, Job: e.Job, Details: e.Details, Outcome: e.Outcome})
	}
	writeJSON(w, http.StatusOK, out)
}

// csvSafe stops a spreadsheet from running a cell that starts like a formula
// (a username or job name is user-controlled).
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
