package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"sentinel/pkg/termtext"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/report"
)

// reportHours reads ?hours=, a whole number from 1 to the longest history, or def when it is not given.
func reportHours(w http.ResponseWriter, r *http.Request, def, most int) (int, bool) {
	text := r.URL.Query().Get("hours")
	if text == "" {
		return def, true
	}
	hours, err := strconv.Atoi(text)
	if err != nil || hours < 1 || hours > most {
		writeError(w, http.StatusBadRequest, "hours must be a number from 1 to "+strconv.Itoa(most))
		return 0, false
	}
	return hours, true
}

// hostReport answers the history of the host of one server.
func (s *Server) hostReport(w http.ResponseWriter, r *http.Request, _ session) {
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		writeError(w, http.StatusBadRequest, "agent is the name of a server")
		return
	}
	hours, ok := reportHours(w, r, 24, report.MaxHours)
	if !ok {
		return
	}
	if !s.agentExists(w, r, agent) {
		return
	}
	points, summary, step, err := s.app.Report.HostHistory(r.Context(), agent, hours, time.Now())
	if err != nil {
		internalError(w, r, err)
		return
	}
	answer := ctlapi.HostReport{Agent: agent, Hours: hours, StepSeconds: int(step / time.Second), Points: make([]ctlapi.HostPoint, 0, len(points)),
		Summary: ctlapi.HostSummary{Samples: summary.Samples, CPUAvg: summary.CPUAvg, CPUMax: summary.CPUMax, MemAvg: summary.MemAvg, MemMax: summary.MemMax, LoadAvg: summary.LoadAvg, DiskMax: summary.DiskMax}}
	for _, point := range points {
		answer.Points = append(answer.Points, ctlapi.HostPoint{At: point.At, CPUPercent: point.CPUPercent, Load1: point.Load1, MemPercent: point.MemPercent, DiskPercent: point.DiskPercent})
	}
	writeJSON(w, http.StatusOK, answer)
}

// agentExists answers 404 for a name that is not a server of this control plane.
func (s *Server) agentExists(w http.ResponseWriter, r *http.Request, name string) bool {
	agents, err := s.app.Enroll.List(r.Context())
	if err != nil {
		internalError(w, r, err)
		return false
	}
	for _, agent := range agents {
		if agent.Name == name {
			return true
		}
	}
	writeError(w, http.StatusNotFound, "no server named "+strconv.Quote(termtext.Shorten(termtext.Line(name), 64)))
	return false
}

// backupReport answers the backups of one server, or of all.
func (s *Server) backupReport(w http.ResponseWriter, r *http.Request, _ session) {
	hours, ok := reportHours(w, r, 7*24, report.MaxBackupHours)
	if !ok {
		return
	}
	agent := r.URL.Query().Get("agent")
	if agent != "" && !s.agentExists(w, r, agent) {
		return
	}
	jobs, failed, err := s.app.Report.Backups(r.Context(), agent, hours, time.Now())
	if err != nil {
		internalError(w, r, err)
		return
	}
	answer := ctlapi.BackupReport{Agent: agent, Hours: hours, Jobs: make([]ctlapi.BackupJob, 0, len(jobs)), Failed: make([]ctlapi.BackupRun, 0, len(failed))}
	for _, job := range jobs {
		answer.Jobs = append(answer.Jobs, ctlapi.BackupJob{Agent: job.Agent, Job: termtext.Line(job.Job), Repository: termtext.Line(job.Repository), Runs: job.Runs, Failures: job.Failures,
			LastFinishedAt: job.LastFinishedAt, LastSuccessAt: job.LastSuccessAt, LastResult: termtext.Line(job.LastResult), BytesAdded: job.BytesAdded})
	}
	for _, run := range failed {
		answer.Failed = append(answer.Failed, ctlapi.BackupRun{Agent: run.Agent, Job: termtext.Line(run.Job), Repository: termtext.Line(run.Repository), FinishedAt: run.FinishedAt, Result: termtext.Line(run.Result)})
	}
	writeJSON(w, http.StatusOK, answer)
}
