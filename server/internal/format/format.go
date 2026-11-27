// Package format writes the things sentinelctl shows, in the words a person reads: how long ago, how
// many bytes, which day. The command line and the terminal screen use the same ones, so they never
// disagree.
package format

import (
	"fmt"
	"time"
)

// Ago says how long before now a time was, or "-" when there is none.
func Ago(t *time.Time, now time.Time) string {
	if t == nil {
		return "-"
	}
	return Since(*t, now)
}

// Since is Ago for a time that is known.
func Since(t, now time.Time) string {
	elapsed := max(now.Sub(t), 0)
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%d giây trước", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%d phút trước", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%d giờ trước", int(elapsed.Hours()))
	}
	return fmt.Sprintf("%d ngày trước", int(elapsed.Hours()/24))
}

// Dash is the text, or "-" when it is empty.
func Dash(text string) string {
	if text == "" {
		return "-"
	}
	return text
}

// Day is the local day of a time, or "-" when there is none.
func Day(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02")
}

// Size writes a number of bytes the way a person reads it: 1.5 GiB.
func Size(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	divisor, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		divisor *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(divisor), "KMGTPE"[exp])
}

// Uptime is a duration as days, hours and minutes: "3 ngày 4 giờ".
func Uptime(seconds int64) string {
	elapsed := time.Duration(min(max(seconds, 0), 100*365*86400)) * time.Second // a machine can report anything
	days, hours, minutes := int(elapsed.Hours())/24, int(elapsed.Hours())%24, int(elapsed.Minutes())%60
	switch {
	case days > 0:
		return fmt.Sprintf("%d ngày %d giờ", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d giờ %d phút", hours, minutes)
	}
	return fmt.Sprintf("%d phút", minutes)
}

// Status is the status of an agent in words: ONLINE and OFFLINE are the words of the alerts; PENDING
// and REVOKED say where the agent is on its way. An empty status comes from a control plane older than
// the field, and state (pending, active or revoked) says what it can.
func Status(status, state string) string {
	switch status {
	case "PENDING":
		return "PENDING (chờ join)"
	case "REVOKED":
		return "REVOKED (đã thu hồi)"
	case "MAINTENANCE":
		return "MAINTENANCE (bảo trì)"
	case "":
		return map[string]string{"pending": "chờ join", "active": "hoạt động", "revoked": "đã thu hồi"}[state]
	}
	return status
}

// Sparkline draws values as a row of block characters, scaled from 0 to top (a value above top is a full block, a
// negative or missing one the lowest). No values make no row.
func Sparkline(values []float64, top float64) string {
	blocks := []rune("▁▂▃▄▅▆▇█")
	line := make([]rune, 0, len(values))
	for _, value := range values {
		index := 0
		if top > 0 && value > 0 {
			index = min(int(value/top*float64(len(blocks))), len(blocks)-1)
		}
		line = append(line, blocks[index])
	}
	return string(line)
}
