package agent

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"sentinel/internal/api"
	"sentinel/internal/store"
)

// Reporter pushes new rows from the local store to the server.
type Reporter struct {
	Client *Client
	Store  *store.Store
	// Info returns the static part of a report (hostname, jobs, checks, config version).
	Info func() api.Report

	cursor     api.Cursor
	haveCursor bool
}

// Send pushes everything newer than the server's cursor, in batches.
func (r *Reporter) Send(ctx context.Context) error {
	for {
		rep := r.Info()
		full := false
		if r.haveCursor {
			var err error
			if rep.BackupRuns, err = r.Store.BackupRunsSince(ctx, r.cursor.BackupRunID, api.MaxBatch); err != nil {
				return err
			}
			if rep.Results, err = r.Store.ChecksSince(ctx, r.cursor.CheckID, api.MaxBatch); err != nil {
				return err
			}
			if rep.Metrics, err = r.Store.MetricsSince(ctx, r.cursor.MetricID, api.MaxBatch); err != nil {
				return err
			}
			full = len(rep.BackupRuns) == api.MaxBatch || len(rep.Results) == api.MaxBatch || len(rep.Metrics) == api.MaxBatch
		}
		// Otherwise this is a heartbeat that tells us where the server is.
		alerts, err := r.Store.Alerts(ctx, true)
		if err != nil {
			return err
		}
		rep.OpenAlerts = alerts

		var resp api.ReportResponse
		if err := r.Client.Do(ctx, http.MethodPost, "/api/agent/report", rep, &resp); err != nil {
			return err
		}
		first := !r.haveCursor
		r.cursor, r.haveCursor = resp.Cursor, true
		if !first && !full {
			return nil
		}
	}
}

// Run sends a report every interval until ctx is cancelled. Failures are
// logged and retried on the next tick; the local store keeps the data.
func (r *Reporter) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	failing := false
	for {
		if err := r.Send(ctx); err != nil && ctx.Err() == nil {
			if !failing {
				slog.Warn("report to server failed", "error", err)
			}
			failing = true
		} else if failing {
			slog.Info("reporting to server again")
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
