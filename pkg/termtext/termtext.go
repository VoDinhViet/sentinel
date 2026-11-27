// Package termtext makes text from outside Sentinel safe to print on a terminal or put in a chat message.
//
// File names in a backed-up folder, restic and database error output, and the body of a webhook answer
// are controlled by other users or by remote servers. Printed raw, an escape sequence in them can move the
// cursor over earlier lines, set the window title, write to the clipboard (OSC 52) or hide text; a right-to-left
// override can make a name look like another one.
package termtext

import "strings"

// Clean returns text without escape sequences (CSI, OSC, DCS and the two-byte kind), control characters
// other than tab and newline, C1 controls and the bidirectional override characters. Invalid UTF-8 becomes
// U+FFFD. Everything else, Vietnamese letters and symbols included, is kept.
func Clean(text string) string {
	runes := []rune(text)
	var cleaned strings.Builder
	cleaned.Grow(len(text))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == 0x1b:
			i = endOfEscape(runes, i)
		case r == '\t' || r == '\n':
			cleaned.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// control character: dropped
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			// bidirectional override or isolate: dropped
		default:
			cleaned.WriteRune(r)
		}
	}
	return cleaned.String()
}

// Shorten returns text cut to at most limit runes, with … in place of what was cut. Text that a remote
// server chooses (a version, an error) would otherwise let it fill a chat message or the alert store.
func Shorten(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:max(limit-1, 0)]) + "…"
}

// endOfEscape returns the index of the last rune of the escape sequence that starts with the ESC at runes[start].
func endOfEscape(runes []rune, start int) int {
	if start+1 >= len(runes) {
		return start
	}
	switch runes[start+1] {
	case '[': // CSI: parameters and intermediates, then a final byte 0x40-0x7e
		for j := start + 2; j < len(runes); j++ {
			switch r := runes[j]; {
			case r >= 0x40 && r <= 0x7e:
				return j
			case r < 0x20 || r > 0x7e:
				return j - 1 // not part of a sequence: a terminal gives up here, so the rune is handled as text
			}
		}
		return len(runes) - 1
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: a string that ends at BEL or ST (ESC \)
		for j := start + 2; j < len(runes); j++ {
			if runes[j] == '\n' {
				return j - 1 // never closed: stop at the end of the line instead of eating the rest of the message
			}
			if runes[j] == 0x07 {
				return j
			}
			if runes[j] == 0x1b && j+1 < len(runes) && runes[j+1] == '\\' {
				return j + 1
			}
		}
		return len(runes) - 1
	}
	return start + 1 // two-byte sequence such as ESC c
}
