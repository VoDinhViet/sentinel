package server

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sentinel/internal/api"
	"sentinel/internal/server/service"
)

func (s *Server) routesUI() {
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(staticFS()))))
	s.mux.HandleFunc("GET /login", s.handleLoginForm)
	s.mux.HandleFunc("POST /login", s.handleLogin)
	s.mux.HandleFunc("POST /logout", s.requireLogin(s.handleLogout))

	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/manage", http.StatusSeeOther)
	})
	s.mux.HandleFunc("GET /manage", s.requireLogin(s.handleManage))
	s.mux.HandleFunc("GET /manage/grid", s.requireLogin(s.handleManageGrid))

	s.mux.HandleFunc("GET /agents/new", s.requireLogin(s.handleAgentNew))
	s.mux.HandleFunc("POST /agents", s.requireLogin(s.handleAgentCreate))
	s.mux.HandleFunc("GET /agents/{id}", s.requireLogin(s.withAgentID(s.handleAgent)))
	s.mux.HandleFunc("GET /agents/{id}/live", s.requireLogin(s.withAgentID(s.handleAgentLive)))
	s.mux.HandleFunc("GET /agents/{id}/metrics.json", s.requireLogin(s.withAgentID(s.handleMetrics)))
	s.mux.HandleFunc("POST /agents/{id}/rotate", s.requireLogin(s.withAgentID(s.handleRotate)))
	s.mux.HandleFunc("POST /agents/{id}/delete", s.requireLogin(s.withAgentID(s.handleDelete)))

	s.mux.HandleFunc("POST /agents/{id}/backup", s.requireLogin(s.withAgentID(s.handleBackupNow)))
	s.mux.HandleFunc("POST /agents/{id}/snapshots", s.requireLogin(s.withAgentID(s.handleSnapshots)))
	s.mux.HandleFunc("GET /agents/{id}/commands/{cid}/snapshots", s.requireLogin(s.withAgentID(s.handleSnapshotsResult)))
	s.mux.HandleFunc("POST /agents/{id}/restore", s.requireLogin(s.withAgentID(s.handleRestore)))

	s.routesConfig()
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		h.ServeHTTP(w, r)
	})
}

const uiAgentKey ctxKey = iota + 200

// withAgentID loads the agent named by the {id} path value.
func (s *Server) withAgentID(h func(http.ResponseWriter, *http.Request, service.Agent)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		a, err := s.Service.GetAgent(r.Context(), id)
		if errors.Is(err, service.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		h(w, r, a)
	}
}

type agentCard struct {
	service.AgentStatus
	Online bool
}

func (s *Server) overview(r *http.Request) (map[string]any, error) {
	agents, err := s.Service.ListAgents(r.Context())
	if err != nil {
		return nil, err
	}
	var cards []agentCard
	for _, a := range agents {
		st, err := s.Service.Status(r.Context(), a.ID)
		if err != nil {
			return nil, err
		}
		cards = append(cards, agentCard{AgentStatus: st, Online: a.Online(time.Now(), s.Config.OfflineAfter)})
	}
	return map[string]any{"Agents": cards}, nil
}

func (s *Server) handleManage(w http.ResponseWriter, r *http.Request) {
	data, err := s.overview(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.render(w, r, "manage.html", data)
}

func (s *Server) handleManageGrid(w http.ResponseWriter, r *http.Request) {
	data, err := s.overview(r)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.renderPartial(w, r, "manage.html", "grid", data)
}

func (s *Server) handleAgentNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "agent_new.html", map[string]any{"Name": "", "Error": ""})
}

func (s *Server) handleAgentCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	a, token, err := s.Service.CreateAgent(r.Context(), name)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, r, "agent_new.html", map[string]any{"Name": name, "Error": err.Error()})
		return
	}
	s.renderToken(w, r, a, token)
}

func (s *Server) renderToken(w http.ResponseWriter, r *http.Request, a service.Agent, token string) {
	public := s.Config.PublicURL
	if public == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		public = scheme + "://" + r.Host
	}
	s.render(w, r, "agent_token.html", map[string]any{"Agent": a, "Token": token, "PublicURL": public})
}

func (s *Server) liveData(r *http.Request, a service.Agent) (map[string]any, error) {
	st, err := s.Service.Status(r.Context(), a.ID)
	if err != nil {
		return nil, err
	}
	cmds, err := s.Service.ListCommands(r.Context(), a.ID, 10)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"Status":   st,
		"Online":   a.Online(time.Now(), s.Config.OfflineAfter),
		"Commands": cmds,
	}, nil
}

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request, a service.Agent) {
	data, err := s.liveData(r, a)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.render(w, r, "agent.html", data)
}

func (s *Server) handleAgentLive(w http.ResponseWriter, r *http.Request, a service.Agent) {
	data, err := s.liveData(r, a)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.renderPartial(w, r, "agent.html", "live", data)
}

// handleMetrics returns 24h of samples in uPlot's column format.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request, a service.Agent) {
	series, err := s.Service.MetricSeries(r.Context(), a.ID, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := struct {
		T      []int64   `json:"t"`
		CPU    []float64 `json:"cpu"`
		Memory []float64 `json:"memory"`
		Disk   []float64 `json:"disk"`
	}{T: []int64{}, CPU: []float64{}, Memory: []float64{}, Disk: []float64{}}
	for _, m := range series {
		out.T = append(out.T, m.At.Unix())
		out.CPU = append(out.CPU, m.CPU)
		out.Memory = append(out.Memory, m.Memory)
		out.Disk = append(out.Disk, m.DiskMax)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRotate(w http.ResponseWriter, r *http.Request, a service.Agent) {
	token, err := s.Service.RotateToken(r.Context(), a.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.renderToken(w, r, a, token)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, a service.Agent) {
	if err := s.Service.DeleteAgent(r.Context(), a.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, "/manage", http.StatusSeeOther)
}

// enqueue queues a command and re-renders the live panel, with any
// validation error shown above it.
func (s *Server) enqueue(w http.ResponseWriter, r *http.Request, a service.Agent, args any) {
	_, err := s.Service.Enqueue(r.Context(), a.ID, args)
	data, derr := s.liveData(r, a)
	if derr != nil {
		s.internalError(w, r, derr)
		return
	}
	if err != nil {
		data["Error"] = err.Error()
	}
	s.renderPartial(w, r, "agent.html", "live", data)
}

func (s *Server) handleBackupNow(w http.ResponseWriter, r *http.Request, a service.Agent) {
	s.enqueue(w, r, a, api.BackupRunArgs{Job: r.PostFormValue("job")})
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request, a service.Agent) {
	job := r.PostFormValue("job")
	if r.PostFormValue("confirm") != job {
		http.Error(w, "confirmation does not match the job name", http.StatusBadRequest)
		return
	}
	s.enqueue(w, r, a, api.RestoreArgs{Job: job, Repository: r.PostFormValue("repository"), Snapshot: r.PostFormValue("snapshot")})
}

func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request, a service.Agent) {
	job, repo, _ := strings.Cut(r.PostFormValue("target"), "|")
	id, err := s.Service.Enqueue(r.Context(), a.ID, api.SnapshotsArgs{Job: job, Repository: repo})
	if err != nil {
		w.Write([]byte(`<div class="flash bad">` + html.EscapeString(err.Error()) + `</div>`))
		return
	}
	s.renderSnapshots(w, r, a, id)
}

func (s *Server) handleSnapshotsResult(w http.ResponseWriter, r *http.Request, a service.Agent) {
	id, err := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderSnapshots(w, r, a, id)
}

func (s *Server) renderSnapshots(w http.ResponseWriter, r *http.Request, a service.Agent, cmdID int64) {
	cmd, err := s.Service.GetCommand(r.Context(), a.ID, cmdID)
	if errors.Is(err, service.ErrNotFound) || (err == nil && cmd.Type != api.CmdSnapshots) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var args api.SnapshotsArgs
	json.Unmarshal(cmd.Args, &args)
	var snaps []api.Snapshot
	if cmd.Status == api.StatusDone {
		json.Unmarshal(cmd.Output, &snaps)
		// Newest first.
		for i, j := 0, len(snaps)-1; i < j; i, j = i+1, j-1 {
			snaps[i], snaps[j] = snaps[j], snaps[i]
		}
	}
	jobType := ""
	for _, j := range a.Jobs {
		if j.Name == args.Job {
			jobType = j.Type
		}
	}
	s.renderPartial(w, r, "agent.html", "snapshots", map[string]any{
		"AgentID": a.ID, "Command": cmd, "Snapshots": snaps,
		"Job": args.Job, "Repository": args.Repository, "JobType": jobType,
	})
}
