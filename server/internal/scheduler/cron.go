package scheduler

import (
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// Cron is a timetable written the usual way: five fields (minute, hour, day of month, month, day of week),
// each "*", a number, a range "a-b", a list "a,b", or any of those with "/step"; month and weekday also take
// their three-letter English names. Days of the week run 0 to 7 with 0 and 7 both Sunday. When both the day of
// the month and the day of the week are narrowed down a day matches if either does, as in cron. "@hourly",
// "@daily" (and "@midnight"), "@weekly", "@monthly" and "@yearly" ("@annually") stand for the usual lines.
// A leading "CRON_TZ=Zone " puts the timetable in that zone; without it the zone of the control plane is used.
type Cron struct {
	minute, hour, dom, month, dow uint64
	domAny, dowAny                bool
	location                      *time.Location
}

const maxSpecLen = 100

var shortcuts = map[string]string{
	"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *",
	"@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *", "@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *",
}

var (
	monthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	dayNames   = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
)

// ParseCron reads a timetable.
func ParseCron(spec string) (Cron, error) {
	if len(spec) > maxSpecLen {
		return Cron{}, fmt.Errorf("the timetable is longer than %d characters", maxSpecLen)
	}
	cron := Cron{location: time.Local}
	text := strings.TrimSpace(spec)
	if zone, rest, found := strings.Cut(text, " "); found && strings.HasPrefix(zone, "CRON_TZ=") {
		location, err := time.LoadLocation(strings.TrimPrefix(zone, "CRON_TZ="))
		if err != nil {
			return Cron{}, fmt.Errorf("unknown time zone in %q", zone)
		}
		cron.location, text = location, strings.TrimSpace(rest)
	}
	if line, ok := shortcuts[text]; ok {
		text = line
	}
	fields := strings.Fields(text)
	if len(fields) != 5 {
		return Cron{}, errors.New("a timetable has five fields: minute hour day-of-month month day-of-week (or @daily, @hourly, …)")
	}
	var err error
	ranges := []struct {
		name     string
		low, top int
		names    []string
		first    int // the number of the first name
		into     *uint64
	}{
		{"minute", 0, 59, nil, 0, &cron.minute},
		{"hour", 0, 23, nil, 0, &cron.hour},
		{"day of month", 1, 31, nil, 0, &cron.dom},
		{"month", 1, 12, monthNames, 1, &cron.month},
		{"day of week", 0, 7, dayNames, 0, &cron.dow},
	}
	for i, field := range ranges {
		if *field.into, err = parseField(fields[i], field.low, field.top, field.names, field.first); err != nil {
			return Cron{}, fmt.Errorf("%s: %w", field.name, err)
		}
	}
	if cron.dow&(1<<7) != 0 { // 7 is Sunday too
		cron.dow = cron.dow&^(1<<7) | 1
	}
	cron.domAny, cron.dowAny = strings.HasPrefix(fields[2], "*"), strings.HasPrefix(fields[4], "*")
	return cron, nil
}

// parseField reads one field into a set of bits.
func parseField(field string, low, top int, names []string, first int) (uint64, error) {
	var set uint64
	for part := range strings.SplitSeq(field, ",") {
		rangeText, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			number, err := strconv.Atoi(stepText)
			if err != nil || number < 1 || number > top {
				return 0, fmt.Errorf("%q is not a step", part)
			}
			step = number
		}
		from, to := low, top
		if rangeText != "*" {
			fromText, toText, isRange := strings.Cut(rangeText, "-")
			var err error
			if from, err = number(fromText, low, top, names, first); err != nil {
				return 0, err
			}
			to = from
			if isRange {
				if to, err = number(toText, low, top, names, first); err != nil {
					return 0, err
				}
			} else if hasStep {
				to = top // "5/10" is "5-59/10"
			}
			if to < from {
				return 0, fmt.Errorf("%q goes backwards", part)
			}
		}
		for value := from; value <= to; value += step {
			set |= 1 << value
		}
	}
	if bits.OnesCount64(set) == 0 {
		return 0, errors.New("empty")
	}
	return set, nil
}

func number(text string, low, top int, names []string, first int) (int, error) {
	for i, name := range names {
		if strings.EqualFold(text, name) {
			return first + i, nil
		}
	}
	value, err := strconv.Atoi(text)
	if err != nil || value < low || value > top {
		return 0, fmt.Errorf("%q is not a number from %d to %d", text, low, top)
	}
	return value, nil
}

func (c Cron) dayMatches(t time.Time) bool {
	inMonth := c.dom&(1<<t.Day()) != 0
	inWeek := c.dow&(1<<int(t.Weekday())) != 0
	if !c.domAny && !c.dowAny {
		return inMonth || inWeek
	}
	return inMonth && inWeek
}

// Next is the first time after the given one that the timetable names, to the minute, or false when there is
// none in the next five years (30 February). The search is done on the clock of the zone, as a calendar that has
// no daylight saving, so that a gap (a clock put forward) or a repeat (put back) cannot make it loop or name
// a time twice: a time that does not exist that day (02:30 on the day the clock jumps from 02:00 to 03:00) is
// skipped, and one that happens twice (01:30 when the clock goes back from 02:00 to 01:00) is the first.
func (c Cron) Next(after time.Time) (time.Time, bool) {
	local := after.In(c.location)
	year, month, day := local.Date()
	wall := time.Date(year, month, day, local.Hour(), local.Minute()+1, 0, 0, time.UTC)
	end := wall.AddDate(5, 0, 0)
	for wall.Before(end) {
		year, month, day = wall.Date()
		switch {
		case c.month&(1<<int(month)) == 0:
			wall = time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatches(wall):
			wall = time.Date(year, month, day+1, 0, 0, 0, 0, time.UTC)
		case c.hour&(1<<wall.Hour()) == 0:
			wall = time.Date(year, month, day, wall.Hour()+1, 0, 0, 0, time.UTC)
		case c.minute&(1<<wall.Minute()) == 0:
			wall = wall.Add(time.Minute)
		default:
			at := time.Date(year, month, day, wall.Hour(), wall.Minute(), 0, 0, c.location)
			if at.Hour() == wall.Hour() && at.Minute() == wall.Minute() && at.After(after) { // not in a gap, and not before
				return at, true
			}
			wall = wall.Add(time.Minute)
		}
	}
	return time.Time{}, false
}
