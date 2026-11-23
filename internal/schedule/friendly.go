package schedule

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// clockText is a time of day written loosely: 2am, 2:30pm, 02.30, 14:00. A bare number is
	// not one (monthly 1 means the first day).
	clockText   = regexp.MustCompile(`^(\d{1,2})(?:[:.](\d{2}))?(am|pm)?$`)
	ordinalText = regexp.MustCompile(`^(\d{1,2})(?:st|nd|rd|th)$`)
	fillerWords = map[string]bool{"at": true, "on": true, "the": true, "of": true, "each": true}
	plainStarts = map[string]bool{"hourly": true, "daily": true, "weekly": true, "monthly": true, "every": true, "everyday": true}
	unitLetters = map[string]string{"h": "h", "hr": "h", "hrs": "h", "hour": "h", "hours": "h", "m": "m", "min": "m", "mins": "m", "minute": "m", "minutes": "m"}
	everyWords  = map[string]string{"day": "daily", "week": "weekly", "month": "monthly", "hour": "hourly"}
)

// friendly rewrites a loosely written plain schedule into the canonical form translate reads:
// "daily at 2:30pm" becomes "daily 14:30", "every 6 hours" "every 6h", "weekly monday 3am"
// "weekly monday 03:00". A cron expression is returned unchanged.
func friendly(spec string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(spec)))
	if len(fields) == 0 || !plainStarts[fields[0]] {
		return spec
	}
	tokens := fields[:1]
	for _, field := range fields[1:] {
		if !fillerWords[field] {
			tokens = append(tokens, field)
		}
	}
	if tokens[0] == "everyday" {
		tokens[0] = "daily"
	}
	if tokens[0] == "every" && len(tokens) >= 2 {
		if word, ok := everyWords[tokens[1]]; ok {
			tokens = append([]string{word}, tokens[2:]...)
		} else if unit, ok := unitLetters[tokens[len(tokens)-1]]; ok && len(tokens) == 3 {
			tokens = []string{"every", tokens[1] + unit}
		}
	}
	for i := 1; i < len(tokens); i++ {
		if match := ordinalText.FindStringSubmatch(tokens[i]); match != nil {
			tokens[i] = match[1]
		} else if clock, ok := looseClock(tokens[i]); ok && tokens[0] != "every" {
			tokens[i] = clock
		}
	}
	return strings.Join(tokens, " ")
}

// looseClock turns 2am, 2:30pm or 02.30 into 02:00, 14:30, 02:30; a bare number is not a time.
func looseClock(text string) (string, bool) {
	match := clockText.FindStringSubmatch(text)
	if match == nil || (match[2] == "" && match[3] == "" && !strings.ContainsAny(text, ":.")) {
		return "", false
	}
	hour, _ := strconv.Atoi(match[1])
	minute := 0
	if match[2] != "" {
		minute, _ = strconv.Atoi(match[2])
	}
	switch match[3] {
	case "am":
		hour %= 12
	case "pm":
		hour = hour%12 + 12
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), true
}

// shape is a plain schedule taken apart, so a screen can step through its parts.
type shape struct {
	kind         string // manual, hourly, daily, weekly, monthly, every or other (a cron expression)
	hour, minute int
	weekday, day int
	every        time.Duration
}

func shapeOf(spec string) shape {
	if IsManual(spec) {
		return shape{kind: "manual"}
	}
	fields := strings.Fields(friendly(spec))
	other := shape{kind: "other"}
	clockAt := func(index int) (int, int, bool) {
		if index >= len(fields) {
			return 0, 0, false
		}
		hour, minute, err := clock(fields[index])
		return hour, minute, err == nil
	}
	switch fields[0] {
	case "hourly":
		sh := shape{kind: "hourly"}
		if len(fields) == 2 {
			minute, err := strconv.Atoi(strings.TrimPrefix(fields[1], ":"))
			if err != nil {
				return other
			}
			sh.minute = minute
		}
		return sh
	case "daily":
		if hour, minute, ok := clockAt(1); ok && len(fields) == 2 {
			return shape{kind: "daily", hour: hour, minute: minute}
		}
	case "weekly":
		if hour, minute, ok := clockAt(2); ok && len(fields) == 3 {
			for i, name := range weekdayAbbrevs {
				if strings.HasPrefix(fields[1], name) {
					return shape{kind: "weekly", weekday: i, hour: hour, minute: minute}
				}
			}
		}
	case "monthly":
		if hour, minute, ok := clockAt(2); ok && len(fields) == 3 {
			if day, err := strconv.Atoi(fields[1]); err == nil && day >= 1 && day <= 28 {
				return shape{kind: "monthly", day: day, hour: hour, minute: minute}
			}
		}
	case "every":
		if len(fields) == 2 {
			if interval, err := time.ParseDuration(fields[1]); err == nil && interval >= time.Minute {
				return shape{kind: "every", every: interval}
			}
		}
	}
	return other
}

// spec writes the shape in the canonical plain form.
func (sh shape) spec() string {
	switch sh.kind {
	case "hourly":
		if sh.minute == 0 {
			return "hourly"
		}
		return fmt.Sprintf("hourly :%02d", sh.minute)
	case "daily":
		return fmt.Sprintf("daily %02d:%02d", sh.hour, sh.minute)
	case "weekly":
		return fmt.Sprintf("weekly %s %02d:%02d", weekdayAbbrevs[sh.weekday], sh.hour, sh.minute)
	case "monthly":
		return fmt.Sprintf("monthly %d %02d:%02d", sh.day, sh.hour, sh.minute)
	case "every":
		if sh.every%time.Hour == 0 {
			return fmt.Sprintf("every %dh", sh.every/time.Hour)
		}
		return fmt.Sprintf("every %dm", sh.every/time.Minute)
	}
	return "manual"
}

// frequencies are the kinds ← → steps through, from the rarest to the most frequent and back.
var frequencies = []shape{
	{kind: "manual"},
	{kind: "every", every: 15 * time.Minute},
	{kind: "hourly"},
	{kind: "every", every: 6 * time.Hour},
	{kind: "daily"},
	{kind: "weekly"},
	{kind: "monthly"},
}

// intervals are the lengths of `every` that alt+↑ and alt+↓ step through.
var intervals = []time.Duration{5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 4 * time.Hour, 6 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour}

// NextFrequency is the schedule one step along manual, every 15m, hourly, every 6h, daily,
// weekly, monthly (or back), keeping the time of day, weekday and day of month it already
// has. A schedule it cannot read (a cron expression) becomes daily 02:00.
func NextFrequency(spec string, back bool) string {
	current := shapeOf(spec)
	index := -1
	for i, frequency := range frequencies {
		switch {
		case frequency.kind != current.kind:
		case current.kind == "every" && (current.every < time.Hour) != (frequency.every < time.Hour):
		default:
			index = i
		}
	}
	if index < 0 {
		return "daily 02:00"
	}
	delta := 1
	if back {
		delta = len(frequencies) - 1
	}
	next := frequencies[(index+delta)%len(frequencies)]
	hour, minute := current.hour, current.minute
	if current.kind == "manual" || current.kind == "every" { // no time of day to keep
		hour, minute = 2, 0
	}
	next.hour, next.minute = hour, minute
	if next.kind == "weekly" {
		next.weekday = current.weekday
	}
	next.day = 1
	if current.kind == "monthly" {
		next.day = current.day
	}
	return next.spec()
}

// Adjust steps the time of a plain schedule: timeSteps moves a daily, weekly or monthly time
// by 30 minutes each (wrapping at midnight), an hourly minute by 15, an `every` to the next
// listed interval; dayDelta moves the weekday of a weekly schedule (wrapping) or the day of a
// monthly one (stopping at 1 and 28). ok is false when there is nothing to change (manual,
// daily with only a day step, a cron expression).
func Adjust(spec string, timeSteps, dayDelta int) (adjusted string, ok bool) {
	sh := shapeOf(spec)
	switch sh.kind {
	case "daily", "weekly", "monthly":
		if timeSteps != 0 {
			total := ((sh.hour*60+sh.minute+30*timeSteps)%1440 + 1440) % 1440
			sh.hour, sh.minute = total/60, total%60
			ok = true
		}
		if dayDelta != 0 && sh.kind == "weekly" {
			sh.weekday = ((sh.weekday+dayDelta)%7 + 7) % 7
			ok = true
		}
		if dayDelta != 0 && sh.kind == "monthly" {
			sh.day = min(max(sh.day+dayDelta, 1), 28)
			ok = true
		}
	case "hourly":
		if timeSteps != 0 {
			sh.minute = ((sh.minute+15*timeSteps)%60 + 60) % 60
			ok = true
		}
	case "every":
		if timeSteps != 0 {
			sh.every, ok = stepInterval(sh.every, timeSteps)
		}
	}
	if !ok {
		return spec, false
	}
	return sh.spec(), true
}

// stepInterval moves to the next (steps > 0) or previous (steps < 0) listed interval.
func stepInterval(current time.Duration, steps int) (time.Duration, bool) {
	index := 0
	for i, interval := range intervals {
		if interval <= current {
			index = i
		}
	}
	if intervals[index] == current {
		index += steps
	} else if steps > 0 {
		index++
	}
	index = min(max(index, 0), len(intervals)-1)
	return intervals[index], true
}

// NextRuns returns the first n runs after now; it is empty for a manual or invalid spec.
func NextRuns(spec string, now time.Time, n int) []time.Time {
	parsed, err := Parse(spec)
	if err != nil {
		return nil
	}
	var runs []time.Time
	for range n {
		now = parsed.Next(now)
		runs = append(runs, now)
	}
	return runs
}
