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

func TestMaxGap(t *testing.T) {
	tests := []struct {
		spec string
		want time.Duration
	}{
		{"*/15 * * * *", 15 * time.Minute},
		{"0 3 * * *", 24 * time.Hour},
		{"0 3 * * 1", 7 * 24 * time.Hour},
		{"0 3 * * 1-5", 3 * 24 * time.Hour}, // Friday to Monday
	}
	for _, tc := range tests {
		got, ok := MaxGap(tc.spec, now)
		if !ok || got != tc.want {
			t.Errorf("MaxGap(%q) = %v %v, want %v", tc.spec, got, ok, tc.want)
		}
	}
	if _, ok := MaxGap("", now); ok {
		t.Error("MaxGap accepted an empty schedule")
	}
}

func TestPlainFormsMeanTheSameAsCron(t *testing.T) {
	tests := []struct {
		spec, cron, description string
	}{
		{"daily 02:00", "0 2 * * *", "every day at 02:00"},
		{"  Daily   2:05 ", "5 2 * * *", "every day at 02:05"},
		{"hourly", "0 * * * *", "every hour at minute 00"},
		{"hourly :15", "15 * * * *", "every hour at minute 15"},
		{"weekly sun 03:00", "0 3 * * 0", "every Sunday at 03:00"},
		{"weekly Monday 23:30", "30 23 * * 1", "every Monday at 23:30"},
		{"monthly 1 04:00", "0 4 1 * *", "on day 1 of every month at 04:00"},
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
	if gap, ok := MaxGap("every 6h", now); !ok || gap != 6*time.Hour {
		t.Errorf("MaxGap(every 6h) = %v %v", gap, ok)
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
	if got := Describe(""); got != "only by hand" {
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
