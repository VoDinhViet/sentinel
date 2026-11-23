// Package schedule answers questions about cron specs shared by the agent
// (stale-backup alerts) and the server (next run, overdue status).
package schedule

import (
	"time"

	"github.com/robfig/cron/v3"
)

// Next returns the first run after now; ok is false for an empty or invalid spec.
func Next(spec string, now time.Time) (time.Time, bool) {
	if spec == "" {
		return time.Time{}, false
	}
	s, err := cron.ParseStandard(spec)
	if err != nil {
		return time.Time{}, false
	}
	return s.Next(now), true
}

// MaxGap returns the longest gap between consecutive runs over the next two
// weeks of runs, which covers weekly and weekday-only schedules.
func MaxGap(spec string, now time.Time) (time.Duration, bool) {
	if spec == "" {
		return 0, false
	}
	s, err := cron.ParseStandard(spec)
	if err != nil {
		return 0, false
	}
	var gap time.Duration
	t := s.Next(now)
	for i := 0; i < 14; i++ {
		next := s.Next(t)
		gap = max(gap, next.Sub(t))
		t = next
	}
	return gap, true
}
