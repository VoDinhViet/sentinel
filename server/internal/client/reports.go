package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"sentinel/pkg/termtext"
	"sentinel/server/internal/ctlapi"
)

// HostReport is the history of the host of a server over the last hours.
func (c *Client) HostReport(ctx context.Context, agent string, hours int) (ctlapi.HostReport, error) {
	var answer ctlapi.HostReport
	query := url.Values{"agent": {agent}, "hours": {strconv.Itoa(hours)}}
	err := c.do(ctx, http.MethodGet, ctlapi.RouteReportHost+"?"+query.Encode(), nil, &answer)
	answer.Agent = termtext.Line(answer.Agent)
	return answer, err
}

// BackupReport is the backups of a server, or of all with "", over the last hours.
func (c *Client) BackupReport(ctx context.Context, agent string, hours int) (ctlapi.BackupReport, error) {
	var answer ctlapi.BackupReport
	query := url.Values{"hours": {strconv.Itoa(hours)}}
	if agent != "" {
		query.Set("agent", agent)
	}
	err := c.do(ctx, http.MethodGet, ctlapi.RouteReportBackups+"?"+query.Encode(), nil, &answer)
	for i := range answer.Jobs {
		job := &answer.Jobs[i]
		job.Agent, job.Job, job.Repository, job.LastResult = termtext.Line(job.Agent), termtext.Line(job.Job), termtext.Line(job.Repository), termtext.Line(job.LastResult)
	}
	for i := range answer.Failed {
		run := &answer.Failed[i]
		run.Agent, run.Job, run.Repository, run.Result = termtext.Line(run.Agent), termtext.Line(run.Job), termtext.Line(run.Repository), termtext.Line(run.Result)
	}
	return answer, err
}
