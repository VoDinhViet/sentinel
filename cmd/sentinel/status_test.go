package main

import (
	"strings"
	"testing"
	"time"
)

func TestRelativeAgo(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{10 * time.Second, "vừa xong"},
		{5 * time.Minute, "5 phút trước"},
		{59 * time.Minute, "59 phút trước"},
		{3 * time.Hour, "3 giờ trước"},
		{47 * time.Hour, "47 giờ trước"},
		{72 * time.Hour, "3 ngày trước"},
	} {
		if got := relativeAgo(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("relativeAgo(%v ago) = %q, want %q", tc.ago, got, tc.want)
		}
	}
	if got := relativeAgo(time.Time{}, now); got != "chưa có" {
		t.Errorf("zero time = %q, want chưa có", got)
	}
}

func TestDaemonStateSaysWhatToDoWhenItIsNotRunning(t *testing.T) {
	running, stopped := true, false
	for name, tc := range map[string]struct {
		running *bool
		want    string
	}{
		"running":   {&running, "running"},
		"stopped":   {&stopped, "NOT RUNNING"},
		"not known": {nil, "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := daemonState(tc.running); !strings.HasPrefix(got, tc.want) {
				t.Errorf("daemonState = %q, want it to start with %q", got, tc.want)
			}
		})
	}
	if got := daemonState(&stopped); !strings.Contains(got, "systemctl start sentinel") {
		t.Errorf("daemonState = %q, want the command to start it", got)
	}
}
