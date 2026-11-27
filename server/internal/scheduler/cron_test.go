package scheduler

import (
	"strings"
	"testing"
	"time"
)

func at(text string) time.Time {
	parsed, err := time.Parse("2006-01-02 15:04", text)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestCronNamesTheNextMinute(t *testing.T) {
	for _, tc := range []struct{ spec, after, want string }{
		{"* * * * *", "2026-11-27 10:00", "2026-11-27 10:01"},
		{"*/15 * * * *", "2026-11-27 10:00", "2026-11-27 10:15"},
		{"0 3 * * *", "2026-11-27 03:00", "2026-11-28 03:00"},
		{"30 2 * * 1-5", "2026-11-27 12:00", "2026-11-30 02:30"}, // a Friday: the next weekday is Monday
		{"@daily", "2026-11-27 10:00", "2026-11-28 00:00"},
		{"@hourly", "2026-11-27 10:30", "2026-11-27 11:00"},
		{"@weekly", "2026-11-27 10:00", "2026-11-29 00:00"}, // Sunday
		{"@monthly", "2026-11-27 10:00", "2026-12-01 00:00"},
		{"@yearly", "2026-11-27 10:00", "2027-01-01 00:00"},
		{"0 0 1,15 * *", "2026-11-02 00:00", "2026-11-15 00:00"},
		{"0 4 * jan,jul *", "2026-11-27 00:00", "2027-01-01 04:00"},
		{"0 4 * * sun", "2026-11-27 00:00", "2026-11-29 04:00"},
		{"0 4 * * 7", "2026-11-27 00:00", "2026-11-29 04:00"}, // 7 is Sunday too
		{"10-20/5 8 * * *", "2026-11-27 08:10", "2026-11-27 08:15"},
		{"5/20 * * * *", "2026-11-27 10:06", "2026-11-27 10:25"}, // 5, 25, 45
		{"0 0 29 2 *", "2026-11-27 00:00", "2028-02-29 00:00"},
		// A day of the month and a day of the week that are both named match either.
		{"0 0 13 * fri", "2026-11-14 00:00", "2026-11-20 00:00"},
	} {
		cron, err := ParseCron("CRON_TZ=UTC " + tc.spec)
		if err != nil {
			t.Errorf("%q: %v", tc.spec, err)
			continue
		}
		got, ok := cron.Next(at(tc.after))
		if !ok || !got.Equal(at(tc.want)) {
			t.Errorf("%q after %s: %v, want %s", tc.spec, tc.after, got, tc.want)
		}
	}
}

func TestCronInATimeZone(t *testing.T) {
	cron, err := ParseCron("CRON_TZ=Asia/Ho_Chi_Minh 0 2 * * *")
	if err != nil {
		t.Skip("no time zone data here: ", err)
	}
	got, _ := cron.Next(at("2026-11-27 10:00"))
	if want := at("2026-11-27 19:00"); !got.Equal(want) { // 02:00 on the 28th at +07 is 19:00 on the 27th
		t.Errorf("%v, want %v", got, want)
	}
}

func TestCronRefusesWhatItDoesNotUnderstand(t *testing.T) {
	for spec, words := range map[string]string{
		"":                             "five fields",
		"* * * *":                      "five fields",
		"* * * * * *":                  "five fields",
		"60 * * * *":                   "minute",
		"* 24 * * *":                   "hour",
		"* * 0 * *":                    "day of month",
		"* * * 13 *":                   "month",
		"* * * * 8":                    "day of week",
		"5-1 * * * *":                  "backwards",
		"*/0 * * * *":                  "step",
		"a * * * *":                    "minute",
		"CRON_TZ=Nowhere/City * * * *": "time zone",
		"@sometimes":                   "five fields",
		"* * * * * ; id":               "five fields",
		strings.Repeat("*", 200):       "longer",
	} {
		if _, err := ParseCron(spec); err == nil || !strings.Contains(err.Error(), words) {
			t.Errorf("%q: %v, want an error with %q", spec, err, words)
		}
	}
}

func TestCronThatNeverComesHasNoNext(t *testing.T) {
	cron, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := cron.Next(at("2026-11-27 00:00")); ok {
		t.Errorf("30 February came: %v", got)
	}
}

func TestDueAtLooksBackNoFurtherThanCatchUp(t *testing.T) {
	cron, _ := ParseCron("CRON_TZ=UTC */5 * * * *")
	now := at("2026-11-27 10:07")
	if due, ok := dueAt(cron, at("2026-11-27 10:00"), now); !ok || !due.Equal(at("2026-11-27 10:05")) {
		t.Errorf("one run was due: %v %v", due, ok)
	}
	if _, ok := dueAt(cron, at("2026-11-27 10:05"), now); ok {
		t.Error("a run that was done was due again")
	}
	// Down for a day: only the latest run in the last ten minutes, once.
	if due, ok := dueAt(cron, at("2026-11-26 10:00"), now); !ok || !due.Equal(at("2026-11-27 10:05")) {
		t.Errorf("after a long stop: %v %v", due, ok)
	}
	daily, _ := ParseCron("CRON_TZ=UTC 0 3 * * *")
	if _, ok := dueAt(daily, at("2026-11-26 03:00"), at("2026-11-27 03:30")); ok {
		t.Error("a run missed by half an hour was started late")
	}
}

func TestCronAcrossDaylightSavingTime(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone data here: ", err)
	}
	next := func(spec string, after time.Time) time.Time {
		t.Helper()
		cron, err := ParseCron("CRON_TZ=America/New_York " + spec)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := cron.Next(after)
		if !ok {
			t.Fatalf("%q after %v: no next run", spec, after)
		}
		return got
	}
	// The clock jumps from 02:00 to 03:00 on 2026-03-08: a run at 02:30 does not exist that day and is skipped.
	if got := next("30 2 * * *", time.Date(2026, 3, 7, 12, 0, 0, 0, zone)); !got.Equal(time.Date(2026, 3, 9, 2, 30, 0, 0, zone)) {
		t.Errorf("a time in the gap: %v", got)
	}
	// A run just after the gap is on time, found from before the gap and from inside it.
	if got := next("0 3 * * *", time.Date(2026, 3, 8, 1, 50, 0, 0, zone)); !got.Equal(time.Date(2026, 3, 8, 3, 0, 0, 0, zone)) {
		t.Errorf("the hour after the gap: %v", got)
	}
	// A weekly run on the Sunday of the change.
	if got := next("0 4 * * 0", time.Date(2026, 3, 5, 0, 0, 0, 0, zone)); !got.Equal(time.Date(2026, 3, 8, 4, 0, 0, 0, zone)) {
		t.Errorf("a weekly run on the day of the change: %v", got)
	}
	// The clock goes back from 02:00 to 01:00 on 2026-11-01: 01:30 happens twice and is run once.
	first := next("30 1 * * *", time.Date(2026, 10, 31, 12, 0, 0, 0, zone))
	if !first.Equal(time.Date(2026, 11, 1, 1, 30, 0, 0, zone)) || first.Format("MST") != "EDT" {
		t.Fatalf("the first 01:30: %v", first)
	}
	if again := next("30 1 * * *", first); !again.Equal(time.Date(2026, 11, 2, 1, 30, 0, 0, zone)) {
		t.Errorf("01:30 ran twice: the next run is %v", again)
	}
	// Whatever the time asked from, the answer is after it.
	for hours := range 24 * 3 {
		after := time.Date(2026, 3, 7, 0, 0, 0, 0, zone).Add(time.Duration(hours) * time.Hour)
		if got := next("*/20 * * * *", after); !got.After(after) {
			t.Fatalf("%v is not after %v", got, after)
		}
	}
}
