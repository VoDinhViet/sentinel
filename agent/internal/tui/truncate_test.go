package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

func TestTruncate(t *testing.T) {
	cases := []struct {
		name, text string
		width      int
		want       string
	}{
		{"fits", "backup", 6, "backup"},
		{"cut", "backup web-01", 8, "backup …"},
		{"width one", "backup", 1, "…"},
		{"width zero", "backup", 0, "…"},
		{"empty", "", 5, ""},
		{"vietnamese keeps whole letters", "không đọc được", 7, "không …"},
		{"wide characters count two cells", "日本語のテキスト", 7, "日本語…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.text, tc.width)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
			}
			if tc.width > 0 && lipgloss.Width(got) > tc.width {
				t.Errorf("%q is wider than %d", got, tc.width)
			}
		})
	}
}

func TestTruncateIsFastOnALongError(t *testing.T) {
	long := strings.Repeat("restic: cannot read /srv/data/file; ", 20000) // 700 KB
	began := time.Now()
	got := truncate(long, 40)
	if took := time.Since(began); took > 500*time.Millisecond {
		t.Errorf("truncate took %v on %d bytes", took, len(long))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("got %q", got)
	}
}
