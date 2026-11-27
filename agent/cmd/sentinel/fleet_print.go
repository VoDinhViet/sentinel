package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"sentinel/agent/internal/fleet"
	"sentinel/agent/internal/status"
	"sentinel/pkg/termtext"
)

// unreachableAlertKey is the key of the alert row that stands for a server that could not be asked.
const unreachableAlertKey = "unreachable"

func printFleetStatus(w io.Writer, answers []fleet.ServerStatus) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "SERVER\tSTATE\tVERSION\tALERTS\tFAILED BACKUPS\tDOWN CHECKS\tRUNNING\tNOTE")
	for _, answer := range answers {
		summary := answer.Summary()
		fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\n", summary.Name, summary.State, orDash(summary.Version),
			summary.Alerts, summary.FailedBackups, summary.DownChecks, summary.Running, summary.Problem)
	}
	return table.Flush()
}

func orDash(text string) string {
	if text == "" {
		return "-"
	}
	return text
}

// fleetServerJSON is one server in `sentinel fleet status -o json`. Report is the same JSON as
// `sentinel status -o json` of that server, and is left out when the server could not be asked.
type fleetServerJSON struct {
	Name      string         `json:"name"`
	URL       string         `json:"url"`
	State     string         `json:"state"`
	Error     string         `json:"error,omitempty"`
	FetchedAt time.Time      `json:"fetched_at"`
	Report    *status.Report `json:"report,omitempty"`
}

func fleetStatusJSON(answers []fleet.ServerStatus) []fleetServerJSON {
	servers := []fleetServerJSON{}
	for _, answer := range answers {
		summary := answer.Summary()
		server := fleetServerJSON{
			Name: answer.Server.Name, URL: answer.Server.URL, State: summary.State, Error: summary.Problem, FetchedAt: answer.FetchedAt,
		}
		if answer.Err == nil {
			server.Report = &answer.Report
		}
		servers = append(servers, server)
	}
	return servers
}

// fleetAlert is an open alert of one server. A server that could not be asked has one too, with
// the key "unreachable", since for the person watching that is an alert.
type fleetAlert struct {
	Server  string    `json:"server"`
	Key     string    `json:"key"`
	Message string    `json:"message"`
	Since   time.Time `json:"since"` // zero for an unreachable server: it is not known since when
}

func fleetAlerts(answers []fleet.ServerStatus) []fleetAlert {
	alerts := []fleetAlert{}
	for _, answer := range answers {
		if answer.Err != nil {
			alerts = append(alerts, fleetAlert{Server: answer.Server.Name, Key: unreachableAlertKey, Message: termtext.Clean(answer.Err.Error())})
			continue
		}
		for _, alert := range answer.Report.Alerts {
			alerts = append(alerts, fleetAlert{
				Server: answer.Server.Name, Key: termtext.Clean(alert.Key), Message: termtext.Clean(alert.Message), Since: alert.Since,
			})
		}
	}
	return alerts
}

func printFleetAlerts(w io.Writer, alerts []fleetAlert, now time.Time) error {
	fmt.Fprintf(w, "Open alerts: %d\n", len(alerts))
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, alert := range alerts {
		since := "unknown"
		if !alert.Since.IsZero() {
			since = ago(alert.Since, now)
		}
		fmt.Fprintf(table, "  %s\t%s\t%s\tsince %s\n", alert.Server, alert.Key, alert.Message, since)
	}
	return table.Flush()
}
