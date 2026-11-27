package format

import (
	"testing"
	"time"
)

func TestAgoSaysHowLongAndDashesNothing(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) *time.Time { value := now.Add(-ago); return &value }
	for want, got := range map[string]string{
		"-":             Ago(nil, now),
		"0 giây trước":  Ago(at(-time.Hour), now), // a clock ahead of ours is not "in the future"
		"30 giây trước": Ago(at(30*time.Second), now),
		"5 phút trước":  Ago(at(5*time.Minute), now),
		"47 giờ trước":  Ago(at(47*time.Hour), now),
		"3 ngày trước":  Ago(at(72*time.Hour), now),
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestSizeUptimeAndDash(t *testing.T) {
	for want, got := range map[string]string{
		"0 B": Size(0), "1023 B": Size(1023), "1.0 KiB": Size(1024), "1.5 GiB": Size(3 << 29),
		"5 phút": Uptime(300), "2 giờ 3 phút": Uptime(2*3600 + 180), "3 ngày 4 giờ": Uptime(3*86400 + 4*3600),
		"-": Dash(""), "x": Dash("x"), "0 phút": Uptime(-5),
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestStatusKeepsTheWordsOfTheAlertsAndExplainsTheRest(t *testing.T) {
	for _, tc := range []struct{ status, state, want string }{
		{"ONLINE", "active", "ONLINE"}, {"OFFLINE", "active", "OFFLINE"},
		{"PENDING", "pending", "PENDING (chờ join)"}, {"REVOKED", "revoked", "REVOKED (đã thu hồi)"},
		{"", "active", "hoạt động"},
	} {
		if got := Status(tc.status, tc.state); got != tc.want {
			t.Errorf("Status(%q, %q) = %q, want %q", tc.status, tc.state, got, tc.want)
		}
	}
}

func TestDay(t *testing.T) {
	if Day(nil) != "-" {
		t.Error("no time is not a dash")
	}
	at := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	if got := Day(&at); got != at.Local().Format("2006-01-02") {
		t.Errorf("Day = %q", got)
	}
}

func TestSparklineScalesFromZeroToTop(t *testing.T) {
	if got := Sparkline([]float64{0, 12.5, 50, 99, 100, 250, -5}, 100); got != "▁▂▅███▁" {
		t.Errorf("%q", got)
	}
	if got := Sparkline(nil, 100); got != "" {
		t.Errorf("%q", got)
	}
	if got := Sparkline([]float64{5, 5}, 0); got != "▁▁" {
		t.Errorf("a top of zero: %q", got)
	}
}
