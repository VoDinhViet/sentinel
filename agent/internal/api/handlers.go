package api

import (
	"context"
	"net/http"
	"time"

	"sentinel/agent/internal/restic"
	"sentinel/agent/internal/status"
)

// slowCallTimeout ends a request that waits for restic or the doctor, before the server's write timeout does.
const slowCallTimeout = 100 * time.Second

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// status is the same JSON as `sentinel status --output json`.
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	report, err := s.backend.Status(r.Context(), time.Now())
	if err != nil {
		fail(w, r, err)
		return
	}
	report.Version = s.opts.Version
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	report, err := s.backend.Status(r.Context(), time.Now())
	if err != nil {
		fail(w, r, err)
		return
	}
	alerts := report.Alerts
	if alerts == nil {
		alerts = []status.Alert{}
	}
	writeJSON(w, http.StatusOK, alerts)
}

// jobEntry is one backup of the config: a job and a repository it goes to.
type jobEntry struct {
	Job        string `json:"job"`
	Repository string `json:"repository"`
}

func (s *Server) jobs(w http.ResponseWriter, _ *http.Request) {
	entries := []jobEntry{}
	for _, jobRepo := range s.backend.JobRepositories() {
		entries = append(entries, jobEntry{Job: jobRepo.Job, Repository: jobRepo.Repository})
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repository")
	if repo == "" {
		writeError(w, http.StatusBadRequest, "the repository query parameter is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), slowCallTimeout)
	defer cancel()
	snaps, err := s.backend.ListSnapshots(ctx, r.PathValue("job"), repo)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snaps)
}

// startedBackup is the answer to a backup that has been started.
type startedBackup struct {
	Job        string `json:"job"`
	Repository string `json:"repository,omitempty"`
	PID        int    `json:"pid"`
	Log        string `json:"log"`
}

func (s *Server) startBackup(w http.ResponseWriter, r *http.Request) {
	jobName, repo := r.PathValue("job"), r.URL.Query().Get("repository")
	pid, logPath, err := s.backend.StartBackup(jobName, repo)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, startedBackup{Job: jobName, Repository: repo, PID: pid, Log: logPath})
}

func (s *Server) stopBackup(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.RequestStop(r.PathValue("job")); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "stop requested"})
}

// finding is one check of the doctor.
type finding struct {
	Name    string `json:"name"`
	Level   string `json:"level"` // "ok", "warning" or "failed"
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	findings := []finding{}
	ctx, cancel := context.WithTimeout(r.Context(), slowCallTimeout)
	defer cancel()
	for _, f := range s.backend.Doctor(ctx) {
		entry := finding{Name: f.Name, Level: "ok", Message: f.Info, Hint: f.Hint}
		switch {
		case f.Err != nil && f.Warn:
			entry.Level, entry.Message = "warning", restic.Redact(f.Err.Error())
		case f.Err != nil:
			entry.Level, entry.Message = "failed", restic.Redact(f.Err.Error())
		}
		findings = append(findings, entry)
	}
	writeJSON(w, http.StatusOK, findings)
}
