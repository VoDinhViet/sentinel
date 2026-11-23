package schedule

import (
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
