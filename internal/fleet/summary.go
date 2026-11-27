package fleet

import (
	"sentinel/internal/status"
	"sentinel/internal/termtext"
)

// States of a server, from the worst: it cannot be asked, it has something to look at, it is fine.
const (
	StateUnreachable = "UNREACHABLE"
	StateAlert       = "ALERT"
	StateOK          = "OK"
)

// Summary is one line about a server: how it is, and the counts that say why.
type Summary struct {
	Name          string
	State         string
	Version       string
	Alerts        int
	FailedBackups int // jobs whose latest run failed
	DownChecks    int
	Running       int    // backups in progress
	Problem       string // why the server could not be asked
}

// Summary reduces what a server answered to one line. Text that came from the server is cleaned
// of control characters, since it is printed on a terminal.
func (answer ServerStatus) Summary() Summary {
	summary := Summary{Name: answer.Server.Name, State: StateOK}
	if answer.Err != nil {
		summary.State = StateUnreachable
		summary.Problem = termtext.Clean(answer.Err.Error())
		return summary
	}
	report := answer.Report
	summary.Version = termtext.Clean(report.Version)
	summary.Alerts = len(report.Alerts)
	summary.FailedBackups = countFailedBackups(report.Jobs)
	summary.DownChecks = countDownChecks(report.Checks)
	summary.Running = len(report.Running)
	if summary.Alerts+summary.FailedBackups+summary.DownChecks > 0 {
		summary.State = StateAlert
	}
	return summary
}

func countFailedBackups(jobs []status.Job) int {
	failed := 0
	for _, job := range jobs {
		if !job.Success {
			failed++
		}
	}
	return failed
}

func countDownChecks(checks []status.Check) int {
	down := 0
	for _, check := range checks {
		if !check.OK {
			down++
		}
	}
	return down
}
