package main

import "testing"

func TestTimeLeftTextSaysNothingApproximateForUnderAMinute(t *testing.T) {
	for seconds, want := range map[int64]string{20: "còn <1 phút", 29: "còn <1 phút", 25 * 60: "còn ~25 phút", 3*3600 + 600: "còn ~3 giờ 10 phút"} {
		if got := timeLeftText(seconds); got != want {
			t.Errorf("%ds: %q, want %q", seconds, got, want)
		}
	}
}
