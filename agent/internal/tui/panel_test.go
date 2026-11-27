package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestPanelIsExactlyAsWideAndTallAsAsked(t *testing.T) {
	body := []string{"short", strings.Repeat("x", 300), "two\nlines", dimStyle.Render(strings.Repeat("y", 300))}
	for _, size := range [][2]int{{20, 5}, {60, 8}, {7, 3}, {120, 4}} {
		width, height := size[0], size[1]
		lines := panel("title", width, height, colourBorder, body)
		if len(lines) != height {
			t.Errorf("%dx%d: %d lines", width, height, len(lines))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got != width {
				t.Errorf("%dx%d: line %d is %d cells wide: %q", width, height, i, got, plain(line))
			}
		}
	}
	if lines := panel("t", 4, 3, colourBorder, nil); lines != nil {
		t.Errorf("a panel too narrow for its borders and a space is dropped: %v", lines)
	}
}

func TestProgressBarFillsInProportion(t *testing.T) {
	for percent, want := range map[float64]string{0: "░░░░░░░░░░", 50: "█████░░░░░", 100: "██████████", 250: "██████████", -5: "░░░░░░░░░░"} {
		if got := plain(progressBar(percent, 10, sevRun)); got != want {
			t.Errorf("%v%%: %q, want %q", percent, got, want)
		}
	}
}

func TestSparklineScalesToThePeakAndMarksFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		values []int64
		failed []bool
		width  int
		want   string
	}{
		"scaled to the peak":   {[]int64{10, 20, 40}, []bool{false, false, false}, 8, "▂▄█"},
		"a failure is a cross": {[]int64{5, 0, 5}, []bool{false, true, false}, 8, "█✗█"},
		"only the latest fit":  {[]int64{40, 40, 10, 40}, nil, 2, "▂█"},
		"nothing but zeros":    {[]int64{0, 0}, nil, 8, "▁▁"},
	} {
		if got := plain(sparkline(tc.values, tc.failed, tc.width)); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestKvColumnsStayWithinTheRoom(t *testing.T) {
	pairs := [][2]string{
		{"Loại", "files"}, {"Nguồn", strings.Repeat("/long/path", 20)}, {"Repository", "local, offsite"},
		{"Lịch", "mỗi ngày lúc 02:00 (mặc định)"}, {"Lần tới", "T3 24/11 02:00"}, {"Giữ", "daily 7 · weekly 4 · monthly 6"}, {"Snapshot", "6639f396 · vừa xong"},
	}
	for _, width := range []int{30, 60, 76, 116} {
		lines := kvColumns(pairs, width, 3)
		if len(lines) > 3 && width >= 76 { // a narrower room trades rows for width; the caller keeps the rows it has
			t.Errorf("width %d: %d rows, want at most 3", width, len(lines))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d: row %d is %d cells wide: %q", width, i, got, plain(line))
			}
		}
	}
	if got := kvColumns(nil, 60, 3); got != nil {
		t.Errorf("no pairs, no rows: %v", got)
	}
	if got := kvColumns(pairs[:2], 116, 3); len(got) != 2 { // 2 pairs in rows of 3: one column, two rows
		t.Errorf("two pairs make two rows, got %d", len(got))
	}
}

func TestWrapLinesCutsAWordLongerThanTheLine(t *testing.T) {
	long := strings.Repeat("a", 45)
	lines := wrapLines("see "+long+" now", 20)
	for _, line := range lines {
		if lipgloss.Width(line) > 20 {
			t.Errorf("line %q is wider than 20", line)
		}
	}
	if joined := strings.ReplaceAll(strings.Join(lines, ""), " ", ""); joined != "see"+long+"now" {
		t.Errorf("text lost: %q", joined)
	}
	if got := wrapLines("", 20); len(got) != 1 || got[0] != "" {
		t.Errorf("empty text is one empty line: %q", got)
	}
}
