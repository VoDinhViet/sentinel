package main

import (
	"testing"
	"time"
)

func TestRelativeAgo(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{59 * time.Minute, "59m ago"},
		{3 * time.Hour, "3h ago"},
		{47 * time.Hour, "47h ago"},
		{72 * time.Hour, "3d ago"},
	} {
		if got := relativeAgo(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("relativeAgo(%v ago) = %q, want %q", tc.ago, got, tc.want)
		}
	}
	if got := relativeAgo(time.Time{}, now); got != "never" {
		t.Errorf("zero time = %q, want never", got)
	}
}
