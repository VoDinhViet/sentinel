package schedule

import "testing"

func TestReadAndSetTime(t *testing.T) {
	tests := []struct {
		spec, label, setTo string
	}{
		{"daily 02:00", "Daily", "daily 03:30"},
		{"weekly mon 03:00", "Weekly", "weekly mon 03:30"},
		{"monthly 5 04:00", "Monthly", "monthly 5 03:30"},
		{"every 6h", "Every 6h", ""},
		{"hourly", "Hourly", ""},
		{"manual", "Manual", ""},
	}
	for _, test := range tests {
		plain, ok := Read(test.spec)
		if !ok || plain.Label != test.label {
			t.Errorf("Read(%q) = %+v, %v; want label %q", test.spec, plain, ok, test.label)
		}
		got, changed := SetTime(test.spec, 3, 30)
		if test.setTo == "" && changed || test.setTo != "" && got != test.setTo {
			t.Errorf("SetTime(%q) = %q, %v; want %q", test.spec, got, changed, test.setTo)
		}
	}
	if _, ok := Read("*/5 * * * *"); ok {
		t.Error("a cron expression is not a plain schedule")
	}
	if got := WeekdayName(1); got != "Thứ Hai" {
		t.Errorf("WeekdayName(1) = %q", got)
	}
}
