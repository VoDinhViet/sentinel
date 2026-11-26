package schedule

import (
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, 3, 4, 10, 30, 0, 0, time.UTC) // a Wednesday

func TestNext(t *testing.T) {
	got, ok := Next("0 3 * * *", now)
	if !ok || !got.Equal(time.Date(2026, 3, 5, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("Next = %v %v", got, ok)
	}
	for _, bad := range []string{"", "not a cron", "* * *"} {
		if _, ok := Next(bad, now); ok {
			t.Errorf("Next(%q) accepted", bad)
		}
	}
}

func TestSecondRunAfter(t *testing.T) {
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC) }
	tests := []struct {
		name string
		spec string
		last time.Time
		want time.Time
	}{
		{"every 15 minutes", "*/15 * * * *", at(5, 10, 7), at(5, 10, 30)},
		{"daily", "0 3 * * *", at(5, 3, 10), at(7, 3, 0)},
		{"weekly", "0 3 * * 1", at(5, 3, 10), at(19, 3, 0)},                      // Monday the 5th
		{"weekdays over the weekend", "0 3 * * 1-5", at(9, 3, 10), at(13, 3, 0)}, // Friday the 9th: Mon 12th, Tue 13th
		{"dense in working hours, last run of the day", "*/15 8-18 * * *", at(5, 18, 45), at(6, 8, 15)},
		{"dense in working hours, inside the day", "*/15 8-18 * * *", at(5, 12, 5), at(5, 12, 30)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := SecondRunAfter(tc.spec, tc.last)
			if !ok || !got.Equal(tc.want) {
				t.Errorf("SecondRunAfter(%q, %v) = %v %v, want %v", tc.spec, tc.last, got, ok, tc.want)
			}
		})
	}
	for _, bad := range []string{"", "manual", "not a cron"} {
		if _, ok := SecondRunAfter(bad, now); ok {
			t.Errorf("SecondRunAfter accepted %q", bad)
		}
	}
}

func TestPlainFormsMeanTheSameAsCron(t *testing.T) {
	tests := []struct {
		spec, cron, description string
	}{
		{"daily 02:00", "0 2 * * *", "mỗi ngày lúc 02:00"},
		{"  Daily   2:05 ", "5 2 * * *", "mỗi ngày lúc 02:05"},
		{"hourly", "0 * * * *", "mỗi giờ vào phút 00"},
		{"hourly :15", "15 * * * *", "mỗi giờ vào phút 15"},
		{"weekly sun 03:00", "0 3 * * 0", "mỗi Chủ nhật lúc 03:00"},
		{"weekly Monday 23:30", "30 23 * * 1", "mỗi Thứ Hai lúc 23:30"},
		{"monthly 1 04:00", "0 4 1 * *", "ngày 1 hằng tháng lúc 04:00"},
		{"0 3 * * 1", "0 3 * * 1", "cron 0 3 * * 1"},
	}
	for _, tc := range tests {
		got, ok := Next(tc.spec, now)
		want, _ := Next(tc.cron, now)
		if !ok || !got.Equal(want) {
			t.Errorf("Next(%q) = %v %v, want %v", tc.spec, got, ok, want)
		}
		if description := Describe(tc.spec); description != tc.description {
			t.Errorf("Describe(%q) = %q, want %q", tc.spec, description, tc.description)
		}
	}
	if got, ok := SecondRunAfter("every 6h", now); !ok || got.Sub(now) < 6*time.Hour || got.Sub(now) > 12*time.Hour {
		t.Errorf("SecondRunAfter(every 6h) = %v %v", got, ok)
	}
	if got, _ := Next("@every 90m", now); !got.Equal(now.Add(90 * time.Minute)) {
		t.Errorf("@every 90m next = %v", got)
	}
}

func TestManualAndBadSpecs(t *testing.T) {
	for _, spec := range []string{"", "  ", "manual", "Manual"} {
		if !IsManual(spec) {
			t.Errorf("IsManual(%q) = false", spec)
		}
		if _, err := Parse(spec); !errors.Is(err, ErrManual) {
			t.Errorf("Parse(%q) error = %v, want ErrManual", spec, err)
		}
		if _, ok := Next(spec, now); ok {
			t.Errorf("Next(%q) accepted a manual schedule", spec)
		}
	}
	if got := Describe(""); got != "chỉ chạy tay" {
		t.Errorf("Describe(\"\") = %q", got)
	}
	for _, bad := range []string{
		"every day", "daily", "daily 25:00", "daily 02", "daily 02:61", "hourly :60", "hourly 1 2",
		"weekly someday 03:00", "weekly sun", "monthly 0 04:00", "monthly 29 04:00", "monthly 1",
		"every", "every 10s", "every soon",
	} {
		if _, err := Parse(bad); err == nil || errors.Is(err, ErrManual) {
			t.Errorf("Parse(%q) accepted or reported manual: %v", bad, err)
		}
		if got := Describe(bad); got != bad {
			t.Errorf("Describe(%q) = %q, want the spec back", bad, got)
		}
	}
}

func TestFriendlySpecsMeanTheCanonicalOnes(t *testing.T) {
	tests := map[string]string{
		"daily at 2:30pm":             "daily 14:30",
		"Daily At 2am":                "daily 02:00",
		"every day 02:00":             "daily 02:00",
		"everyday 3.15":               "daily 03:15",
		"weekly monday 3am":           "weekly monday 03:00",
		"every week on sunday at 4am": "weekly sunday 04:00",
		"monthly on the 1st at 4am":   "monthly 1 04:00",
		"every month 15 23:30":        "monthly 15 23:30",
		"every 6 hours":               "every 6h",
		"every 30 minutes":            "every 30m",
		"every hour":                  "hourly",
		"hourly at :15":               "hourly :15",
		"12am":                        "12am", // not a plain form: left alone
		"0 2 * * *":                   "0 2 * * *",
		"daily 12am":                  "daily 00:00",
		"daily 12pm":                  "daily 12:00",
	}
	for input, want := range tests {
		if got := friendly(input); got != want {
			t.Errorf("friendly(%q) = %q, want %q", input, got, want)
		}
	}
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	for _, spec := range []string{"daily at 2:30pm", "weekly monday 3am", "monthly on the 1st at 4am", "every 6 hours", "every 30 minutes"} {
		if _, err := Parse(spec); err != nil {
			t.Errorf("Parse(%q): %v", spec, err)
		}
	}
	if next, ok := Next("daily at 2:30pm", now); !ok || next.Hour() != 14 || next.Minute() != 30 {
		t.Errorf("next = %v %v", next, ok)
	}
	if got := Describe("weekly monday 3am"); got != "mỗi Thứ Hai lúc 03:00" {
		t.Errorf("describe = %q", got)
	}
}

func TestNextFrequencyKeepsTheTimeAndWraps(t *testing.T) {
	tests := []struct {
		spec string
		back bool
		want string
	}{
		{"manual", false, "every 15m"},
		{"every 15m", false, "hourly"},
		{"hourly", false, "every 6h"},
		{"every 6h", false, "daily 02:00"},
		{"daily 03:30", false, "weekly sun 03:30"},
		{"weekly mon 03:30", false, "monthly 1 03:30"},
		{"monthly 15 03:30", false, "manual"},
		{"manual", true, "monthly 1 02:00"},
		{"daily 03:30", true, "every 6h"},
		{"0 2 * * *", false, "daily 02:00"},
		{"0 2 * * *", true, "daily 02:00"},
	}
	for _, tt := range tests {
		if got := NextFrequency(tt.spec, tt.back); got != tt.want {
			t.Errorf("NextFrequency(%q, back=%v) = %q, want %q", tt.spec, tt.back, got, tt.want)
		}
	}
}

func TestAdjustStepsTimeAndDay(t *testing.T) {
	tests := []struct {
		spec               string
		timeSteps, dayStep int
		want               string
		ok                 bool
	}{
		{"daily 02:00", 1, 0, "daily 02:30", true},
		{"daily 02:00", -1, 0, "daily 01:30", true},
		{"daily 23:30", 1, 0, "daily 00:00", true},
		{"daily 00:00", -1, 0, "daily 23:30", true},
		{"weekly sun 03:00", 0, -1, "weekly sat 03:00", true},
		{"weekly sat 03:00", 0, 1, "weekly sun 03:00", true},
		{"monthly 28 04:00", 0, 1, "monthly 28 04:00", true},
		{"monthly 1 04:00", 0, -1, "monthly 1 04:00", true},
		{"monthly 1 04:00", 0, 1, "monthly 2 04:00", true},
		{"hourly :45", 1, 0, "hourly", true},
		{"every 6h", 1, 0, "every 8h", true},
		{"every 24h", 1, 0, "every 24h", true},
		{"every 20m", 1, 0, "every 30m", true},
		{"every 15m", -1, 0, "every 10m", true},
		{"daily 02:00", 0, 1, "daily 02:00", false},
		{"manual", 1, 0, "manual", false},
		{"0 2 * * *", 1, 0, "0 2 * * *", false},
		{"daily at 2am", 1, 0, "daily 02:30", true},
	}
	for _, tt := range tests {
		got, ok := Adjust(tt.spec, tt.timeSteps, tt.dayStep)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Adjust(%q, %d, %d) = %q, %v; want %q, %v", tt.spec, tt.timeSteps, tt.dayStep, got, ok, tt.want, tt.ok)
		}
	}
}

func TestNextRuns(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	runs := NextRuns("daily 02:00", now, 3)
	if len(runs) != 3 || runs[0].Day() != 24 || runs[1].Day() != 25 || runs[2].Day() != 26 {
		t.Errorf("runs = %v", runs)
	}
	if got := NextRuns("manual", now, 3); len(got) != 0 {
		t.Errorf("manual has no runs: %v", got)
	}
}
