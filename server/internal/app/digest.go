package app

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"sentinel/pkg/notify"
	"sentinel/server/internal/audit"
	"sentinel/server/internal/inventory"
	"sentinel/server/internal/report"
)

// RunDigest sends the daily report at digest_at until ctx is done. With digest_at empty it only waits for ctx:
// serve stops when any of its parts returns, so a part that has nothing to do must not return.
func (a *App) RunDigest(ctx context.Context) error {
	if a.Config.DigestAt == "" {
		<-ctx.Done()
		return nil
	}
	hour, minute, err := report.ParseDigestTime(a.Config.DigestAt)
	if err != nil {
		return fmt.Errorf("digest_at: %w", err)
	}
	notifier := notify.NewMulti(a.Config.Notifiers)
	var sent time.Time
	for {
		// From the later of now and the last send: a clock that was stepped back must not make the same time come twice.
		from := time.Now()
		if sent.After(from) {
			from = sent
		}
		next := report.NextDigest(from, hour, minute)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		sent = next
		a.sendDigestWithRetries(ctx, notifier)
	}
}

// digestRetries is how often, and how long apart, a report that could not be sent is tried again.
const digestRetries = 6

var digestRetryEvery = 10 * time.Minute // a variable so that a test need not wait

func (a *App) sendDigestWithRetries(ctx context.Context, notifier *notify.Multi) {
	for attempt := 1; ; attempt++ {
		err := a.SendDigest(ctx, notifier, time.Now())
		if err == nil || ctx.Err() != nil {
			return
		}
		if attempt > digestRetries {
			slog.Error("send the daily report: given up", "attempts", attempt, "err", err)
			return
		}
		slog.Warn("send the daily report: it is tried again", "in", digestRetryEvery, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(digestRetryEvery):
		}
	}
}

// SendDigest builds the daily report as of now and sends it.
func (a *App) SendDigest(ctx context.Context, notifier *notify.Multi, now time.Time) error {
	agents, err := a.Enroll.List(ctx)
	if err != nil {
		return err
	}
	away, _ := a.Scheduler.ActiveAt(ctx, now) // a window that cannot be read shows the plain status
	var input report.DigestInput
	for _, agent := range agents {
		status := inventory.StatusOf(agent.State, agent.EnrolledAt, agent.LastSeen, now, a.Config.OfflineAfter)
		if status == inventory.Revoked || status == inventory.Pending {
			continue
		}
		if away.Covers(agent.Name) {
			status = inventory.Maintenance
		}
		input.Servers = append(input.Servers, report.ServerLine{Name: agent.Name, Status: string(status)})
	}
	if input.Jobs, input.Failed, err = a.Report.Backups(ctx, "", 24, now); err != nil {
		return err
	}
	// The servers listed are the active ones: the runs of a revoked or pending agent are not in the digest.
	listed := map[string]bool{}
	for _, server := range input.Servers {
		listed[server.Name] = true
	}
	input.Jobs = slices.DeleteFunc(input.Jobs, func(job report.JobSummary) bool { return !listed[job.Agent] })
	input.Failed = slices.DeleteFunc(input.Failed, func(run report.Run) bool { return !listed[run.Agent] })
	if input.StaleAfter = a.Config.DigestStaleAfter; input.StaleAfter > 0 {
		if input.Stale, err = a.Report.Stale(ctx, input.StaleAfter, now); err != nil {
			return err
		}
	}
	open, err := a.AlertStore.Alerts(ctx, true)
	if err != nil {
		return err
	}
	for _, firing := range open {
		input.Alerts = append(input.Alerts, firing.Message)
	}
	message := report.Digest(input, now)
	message.Server = "sentinel-server"
	if err := notifier.Deliver(audit.WithActor(ctx, audit.System), message); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}
