package report

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"sentinel/pkg/notify"
	"sentinel/pkg/termtext"
)

// DigestTime is the hour and minute a digest is sent, as written in server.yaml.
const DigestTime = "15:04"

// ParseDigestTime reads "08:00".
func ParseDigestTime(text string) (hour, minute int, err error) {
	parsed, err := time.Parse(DigestTime, text)
	if err != nil {
		return 0, 0, fmt.Errorf("%q is not a time of day like 08:00", text)
	}
	return parsed.Hour(), parsed.Minute(), nil
}

// NextDigest is the next time after now that the clock says the given time of day, in the zone of now.
func NextDigest(now time.Time, hour, minute int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month(), now.Day()+1, hour, minute, 0, 0, now.Location())
	}
	return next
}

// ServerLine is a server and how it stands.
type ServerLine struct{ Name, Status string }

// DigestInput is what the daily digest says.
type DigestInput struct {
	Servers []ServerLine
	Jobs    []JobSummary // the last 24 hours
	Failed  []Run
	Stale   []StaleJob // jobs with no successful run for StaleAfter
	Alerts  []string   // the open alerts of the control plane, one line each
}

// StaleAfter is how long a job may go without a successful run before the digest names it.
const StaleAfter = 48 * time.Hour

const (
	maxDigestLines = 15
	maxDigestLine  = 160
)

// Digest is the message of the day: how many servers are in each state, the backups that failed, and the alerts that are
// open. It is Info when all is well, Warning when a server is not online or a backup failed.
func Digest(in DigestInput, now time.Time) notify.Message {
	counts := map[string]int{}
	var notWell []string
	for _, server := range in.Servers {
		counts[server.Status]++
		if server.Status != "ONLINE" {
			notWell = append(notWell, server.Name+" "+server.Status)
		}
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Servers: %d", len(in.Servers))
	for _, status := range slices.Sorted(mapKeys(counts)) {
		fmt.Fprintf(&body, ", %d %s", counts[status], status)
	}
	body.WriteString("\n")
	runs, failures := 0, 0
	for _, job := range in.Jobs {
		runs, failures = runs+job.Runs, failures+job.Failures
	}
	fmt.Fprintf(&body, "Backups in the last 24 hours: %d runs, %d failed\n", runs, failures)
	lines(&body, "Not online:", notWell)
	var failed []string
	for _, run := range in.Failed {
		failed = append(failed, fmt.Sprintf("%s %s/%s at %s: %s", run.Agent, run.Job, run.Repository, run.FinishedAt.Format("15:04"), run.Result))
	}
	lines(&body, "Open alerts:", in.Alerts) // before the failures: a message that is too long is cut at the end
	lines(&body, "Failed backups:", failed)
	var stale []string
	for _, job := range in.Stale {
		since := "never succeeded"
		if job.LastSuccessAt != nil {
			since = "last success " + job.LastSuccessAt.Format("2006-01-02 15:04")
		}
		stale = append(stale, fmt.Sprintf("%s %s/%s: %s", job.Agent, job.Job, job.Repository, since))
	}
	lines(&body, "No successful backup for "+StaleAfter.String()+":", stale)
	level := notify.Info
	if len(notWell) > 0 || failures > 0 || len(stale) > 0 {
		level = notify.Warning
	}
	return notify.Message{Level: level, Title: "Daily report " + now.Format("2006-01-02"), Body: strings.TrimRight(body.String(), "\n")}
}

func lines(body *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	body.WriteString(title + "\n")
	for _, item := range items[:min(len(items), maxDigestLines)] {
		body.WriteString("  " + termtext.Shorten(termtext.Line(item), maxDigestLine) + "\n")
	}
	if len(items) > maxDigestLines {
		fmt.Fprintf(body, "  … and %d more\n", len(items)-maxDigestLines)
	}
}

func mapKeys(counts map[string]int) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for key := range counts {
			if !yield(key) {
				return
			}
		}
	}
}
