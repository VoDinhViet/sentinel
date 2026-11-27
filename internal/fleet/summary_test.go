package fleet

import (
	"errors"
	"testing"
	"time"

	"sentinel/internal/status"
)

func TestSummaryOfAServerSaysHowItIs(t *testing.T) {
	server := Server{Name: "web-01"}
	tests := map[string]struct {
		answer ServerStatus
		want   Summary
	}{
		"quiet server": {
			ServerStatus{Server: server, Report: status.Report{Version: "v1", Jobs: []status.Job{{Success: true}}, Checks: []status.Check{{OK: true}}}},
			Summary{Name: "web-01", State: StateOK, Version: "v1"},
		},
		"an open alert": {
			ServerStatus{Server: server, Report: status.Report{Alerts: []status.Alert{{Key: "a"}}}},
			Summary{Name: "web-01", State: StateAlert, Alerts: 1},
		},
		"a failed backup without an alert yet": {
			ServerStatus{Server: server, Report: status.Report{Jobs: []status.Job{{Success: false}, {Success: true}}}},
			Summary{Name: "web-01", State: StateAlert, FailedBackups: 1},
		},
		"a check that is down": {
			ServerStatus{Server: server, Report: status.Report{Checks: []status.Check{{OK: false}}}},
			Summary{Name: "web-01", State: StateAlert, DownChecks: 1},
		},
		"a backup running is not trouble": {
			ServerStatus{Server: server, Report: status.Report{Running: []status.Running{{}}}},
			Summary{Name: "web-01", State: StateOK, Running: 1},
		},
		"unreachable, with the report left empty": {
			ServerStatus{Server: server, Err: errors.New("server web-01: connection refused\x1b[2J"), FetchedAt: time.Now()},
			Summary{Name: "web-01", State: StateUnreachable, Problem: "server web-01: connection refused"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.answer.Summary(); got != tc.want {
				t.Errorf("summary = %+v, want %+v", got, tc.want)
			}
		})
	}
}
