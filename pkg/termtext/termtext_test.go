package termtext

import "testing"

func TestClean(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "backup failed: no space left", "backup failed: no space left"},
		{"vietnamese and symbols", "Đã restore ✓ — 日本語", "Đã restore ✓ — 日本語"},
		{"tab and newline stay", "a\tb\nc", "a\tb\nc"},
		{"colour codes", "\x1b[31mFAILED\x1b[0m", "FAILED"},
		{"csi with parameters and intermediates", "x\x1b[1;38;5;196m\x1b[2 qy", "xy"},
		{"cursor movement hides earlier text", "ok\x1b[2K\x1b[1Gall fine", "okall fine"},
		{"osc 52 clipboard write ended by BEL", "file\x1b]52;c;ZXZpbA==\x07.txt", "file.txt"},
		{"osc window title ended by ST", "a\x1b]0;pwned\x1b\\b", "ab"},
		{"unterminated osc takes the rest of its line", "a\x1b]0;never ends", "a"},
		{"unterminated osc does not eat the next line", "a\x1b]0;never ends\nreal error", "a\nreal error"},
		{"dcs string", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"two byte escape", "a\x1bcb", "ab"},
		{"lone escape at the end", "a\x1b", "a"},
		{"carriage return would overwrite the line", "real\rfake", "realfake"},
		{"bell and backspace", "a\x07b\x08c", "abc"},
		{"nul and delete", "a\x00b\x7fc", "abc"},
		{"c1 controls", "a\u009b31mb\u009d0;x\u0007c", "a31mb0;xc"},
		{"bidi overrides", "gpj\u202etxt.exe", "gpjtxt.exe"},
		{"bidi isolates", "a\u2066b\u2069c", "abc"},
		{"invalid utf-8 becomes the replacement character", "a\xffb", "a�b"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Clean(tt.in); got != tt.want {
				t.Errorf("Clean(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestShortenCutsTextToTheLimit(t *testing.T) {
	tests := map[string]struct {
		text  string
		limit int
		want  string
	}{
		"short enough":  {"abc", 3, "abc"},
		"one over":      {"abcd", 3, "ab…"},
		"counts runes":  {"Việt Nam", 5, "Việt…"},
		"empty":         {"", 3, ""},
		"limit of zero": {"abc", 0, "…"},
		"limit of one":  {"abc", 1, "…"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Shorten(tc.text, tc.limit); got != tc.want {
				t.Errorf("Shorten(%q, %d) = %q, want %q", tc.text, tc.limit, got, tc.want)
			}
		})
	}
}
