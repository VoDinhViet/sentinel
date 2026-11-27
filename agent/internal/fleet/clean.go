package fleet

import (
	"slices"

	"sentinel/agent/internal/status"
	"sentinel/pkg/termtext"
)

// What a server sends is text from outside this machine, and the screens print it. The client
// cleans it once, here, so no screen has to remember to.

// maxNameRunes is the most of a server's own name or version that is kept: they end up in alerts.
const maxNameRunes = 64

// cleanName cleans a short word that a server names itself by, and cuts it to maxNameRunes.
func cleanName(text string) string {
	return termtext.Shorten(termtext.Clean(text), maxNameRunes)
}

// cleanReport returns the report with the control characters and escape sequences of its text removed.
func cleanReport(report status.Report) status.Report {
	report.Version, report.Server = cleanName(report.Version), cleanName(report.Server)
	report.Alerts = slices.Clone(report.Alerts)
	for i, alert := range report.Alerts {
		report.Alerts[i].Key, report.Alerts[i].Message = termtext.Clean(alert.Key), termtext.Clean(alert.Message)
	}
	report.Jobs = slices.Clone(report.Jobs)
	for i, job := range report.Jobs {
		report.Jobs[i] = cleanJob(job)
	}
	report.Checks = slices.Clone(report.Checks)
	for i, check := range report.Checks {
		report.Checks[i].Name, report.Checks[i].Error = termtext.Clean(check.Name), termtext.Clean(check.Error)
	}
	report.Running = slices.Clone(report.Running)
	for i, running := range report.Running {
		report.Running[i].Job, report.Running[i].Repository = termtext.Clean(running.Job), termtext.Clean(running.Repository)
		report.Running[i].Phase, report.Running[i].Summary = termtext.Clean(running.Phase), termtext.Clean(running.Summary)
	}
	return report
}

func cleanJob(job status.Job) status.Job {
	job.Job, job.Repository, job.SnapshotID = termtext.Clean(job.Job), termtext.Clean(job.Repository), termtext.Clean(job.SnapshotID)
	job.Result, job.Warning, job.Error = termtext.Clean(job.Result), termtext.Clean(job.Warning), termtext.Clean(job.Error)
	job.Recent = slices.Clone(job.Recent)
	for i, outcome := range job.Recent {
		job.Recent[i] = termtext.Clean(outcome)
	}
	return job
}
