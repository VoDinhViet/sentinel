package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sentinel/pkg/termtext"
	"sentinel/server/internal/ctlapi"
)

// cleanSchedule strips what a name or a note could carry onto a screen.
func cleanSchedule(one *ctlapi.Schedule) {
	one.Agent, one.Action, one.Spec = termtext.Line(one.Agent), termtext.Line(one.Action), termtext.Line(one.Spec)
	one.CreatedBy, one.LastError = termtext.Line(one.CreatedBy), termtext.Line(one.LastError)
	params := make(map[string]string, len(one.Params))
	for name, value := range one.Params {
		params[termtext.Line(name)] = termtext.Line(value)
	}
	one.Params = params
}

// AddSchedule makes a schedule for a server: spec is a cron timetable.
func (c *Client) AddSchedule(ctx context.Context, agent, spec, action string, params map[string]string) (ctlapi.Schedule, error) {
	var made ctlapi.Schedule
	err := c.do(ctx, http.MethodPost, ctlapi.RouteSchedules, ctlapi.CreateScheduleRequest{Agent: agent, Spec: spec, Action: action, Params: params}, &made)
	cleanSchedule(&made)
	return made, err
}

// Schedules are the schedules of one server, or (with "") of all.
func (c *Client) Schedules(ctx context.Context, agent string) ([]ctlapi.Schedule, error) {
	var found []ctlapi.Schedule
	route := ctlapi.RouteSchedules
	if agent != "" {
		route += "?" + url.Values{"agent": {agent}}.Encode()
	}
	err := c.do(ctx, http.MethodGet, route, nil, &found)
	for i := range found {
		cleanSchedule(&found[i])
	}
	return found, err
}

// RemoveSchedule deletes a schedule.
func (c *Client) RemoveSchedule(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, strings.Replace(ctlapi.RouteSchedule, "{id}", strconv.FormatInt(id, 10), 1), nil, nil)
}

// AddWindow opens a maintenance window for one server, or for all with "". A nil start means now.
func (c *Client) AddWindow(ctx context.Context, agent string, start *time.Time, end time.Time, reason string) (ctlapi.Window, error) {
	var made ctlapi.Window
	err := c.do(ctx, http.MethodPost, ctlapi.RouteMaintenance, ctlapi.CreateWindowRequest{Agent: agent, StartsAt: start, EndsAt: end, Reason: reason}, &made)
	cleanWindow(&made)
	return made, err
}

// Windows are the maintenance windows that are open or still to come.
func (c *Client) Windows(ctx context.Context) ([]ctlapi.Window, error) {
	var found []ctlapi.Window
	err := c.do(ctx, http.MethodGet, ctlapi.RouteMaintenance, nil, &found)
	for i := range found {
		cleanWindow(&found[i])
	}
	return found, err
}

func cleanWindow(one *ctlapi.Window) {
	one.Agent, one.Reason, one.CreatedBy = termtext.Line(one.Agent), termtext.Line(one.Reason), termtext.Line(one.CreatedBy)
}

// RemoveWindow deletes a window, which ends it at once if it is open.
func (c *Client) RemoveWindow(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, strings.Replace(ctlapi.RouteWindow, "{id}", strconv.FormatInt(id, 10), 1), nil, nil)
}
