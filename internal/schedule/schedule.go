// Package schedule answers questions about schedules shared by the daemon, the config
// check and the interactive screen. A schedule is either a cron spec (5 fields, @daily,
// @every 1h, CRON_TZ=...) or one of these plain forms:
//
//	manual                  only run by hand (same as an empty spec)
//	hourly                  at minute 0 of every hour
//	hourly :15              at minute 15 of every hour
//	daily 02:00             every day at 02:00
//	weekly sun 03:00        every Sunday at 03:00
//	monthly 1 04:00         on the 1st of every month at 04:00 (day 1 to 28)
//	every 6h                every 6 hours (at least 1m)
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// ErrManual is returned by Parse for a schedule that never fires on its own.
var ErrManual = errors.New("manual schedule: only run by hand")

var weekdayAbbrevs = [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// IsManual reports whether spec never fires on its own: empty or "manual".
func IsManual(spec string) bool {
	spec = strings.ToLower(strings.TrimSpace(spec))
	return spec == "" || spec == "manual"
}

// Parse turns a spec into a schedule. A manual spec returns ErrManual.
func Parse(spec string) (cron.Schedule, error) {
	if IsManual(spec) {
		return nil, ErrManual
	}
	expression, _, err := translate(spec)
	if err != nil {
		return nil, err
	}
	cronSchedule, err := cron.ParseStandard(expression)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: %w", spec, err)
	}
	return cronSchedule, nil
}

// Describe says in words when a spec runs, e.g. "every day at 02:00". A spec that does not
// parse is returned as it is.
func Describe(spec string) string {
	if IsManual(spec) {
		return "only by hand"
	}
	if _, description, err := translate(spec); err == nil {
		return description
	}
	return strings.TrimSpace(spec)
}

// translate returns the cron expression of a spec and a description of it.
func translate(spec string) (expression, description string, err error) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(spec)))
	invalid := func(format string, args ...any) (string, string, error) {
		return "", "", fmt.Errorf("schedule %q: %s", spec, fmt.Sprintf(format, args...))
	}
	trimmedSpec := strings.TrimSpace(spec)
	switch fields[0] {
	case "hourly":
		minute := 0
		if len(fields) > 2 {
			return invalid("use `hourly` or `hourly :15`")
		}
		if len(fields) == 2 {
			parsedMinute, err := strconv.Atoi(strings.TrimPrefix(fields[1], ":"))
			if err != nil || parsedMinute < 0 || parsedMinute > 59 {
				return invalid("the minute must be 0 to 59, e.g. `hourly :15`")
			}
			minute = parsedMinute
		}
		return fmt.Sprintf("%d * * * *", minute), fmt.Sprintf("every hour at minute %02d", minute), nil
	case "daily":
		if len(fields) != 2 {
			return invalid("use `daily 02:00`")
		}
		hour, minute, err := clock(fields[1])
		if err != nil {
			return invalid("%v", err)
		}
		return fmt.Sprintf("%d %d * * *", minute, hour), fmt.Sprintf("every day at %02d:%02d", hour, minute), nil
	case "weekly":
		if len(fields) != 3 {
			return invalid("use `weekly sun 03:00`")
		}
		day := -1
		for i, name := range weekdayAbbrevs {
			if strings.HasPrefix(fields[1], name) {
				day = i
			}
		}
		if day < 0 {
			return invalid("%q is not a weekday (sun mon tue wed thu fri sat)", fields[1])
		}
		hour, minute, err := clock(fields[2])
		if err != nil {
			return invalid("%v", err)
		}
		return fmt.Sprintf("%d %d * * %d", minute, hour, day), fmt.Sprintf("every %s at %02d:%02d", weekdayNames[day], hour, minute), nil
	case "monthly":
		if len(fields) != 3 {
			return invalid("use `monthly 1 04:00`")
		}
		day, err := strconv.Atoi(fields[1])
		if err != nil || day < 1 || day > 28 {
			return invalid("the day of the month must be 1 to 28")
		}
		hour, minute, err := clock(fields[2])
		if err != nil {
			return invalid("%v", err)
		}
		return fmt.Sprintf("%d %d %d * *", minute, hour, day), fmt.Sprintf("on day %d of every month at %02d:%02d", day, hour, minute), nil
	case "every":
		if len(fields) != 2 {
			return invalid("use `every 6h`")
		}
		interval, err := time.ParseDuration(fields[1])
		if err != nil || interval < time.Minute {
			return invalid("the interval must be a duration of at least 1m, e.g. `every 6h`")
		}
		return "@every " + interval.String(), "every " + interval.String(), nil
	}
	return trimmedSpec, "cron " + trimmedSpec, nil
}

// clock parses "HH:MM" (24 hours).
func clock(text string) (hour, minute int, err error) {
	hourText, minuteText, found := strings.Cut(text, ":")
	hour, hourErr := strconv.Atoi(hourText)
	minute, minuteErr := strconv.Atoi(minuteText)
	if !found || hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("%q is not a time like 02:00", text)
	}
	return hour, minute, nil
}

// Next returns the first run after now; ok is false for a manual or invalid spec.
func Next(spec string, now time.Time) (time.Time, bool) {
	cronSchedule, err := Parse(spec)
	if err != nil {
		return time.Time{}, false
	}
	return cronSchedule.Next(now), true
}

// MaxGap returns the longest gap between consecutive runs over the next two
// weeks of runs, which covers weekly and weekday-only schedules.
func MaxGap(spec string, now time.Time) (time.Duration, bool) {
	cronSchedule, err := Parse(spec)
	if err != nil {
		return 0, false
	}
	var gap time.Duration
	runAt := cronSchedule.Next(now)
	for range 14 {
		next := cronSchedule.Next(runAt)
		gap = max(gap, next.Sub(runAt))
		runAt = next
	}
	return gap, true
}
