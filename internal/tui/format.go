package tui

import (
	"fmt"
	"time"
)

// relativeAgo is a short "5 phút trước" for the interactive screen.
func relativeAgo(t, now time.Time) string {
	elapsed := now.Sub(t)
	switch {
	case t.IsZero():
		return "chưa có"
	case elapsed < time.Minute:
		return "vừa xong"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d phút trước", int(elapsed/time.Minute))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%d giờ trước", int(elapsed/time.Hour))
	}
	return fmt.Sprintf("%d ngày trước", int(elapsed/(24*time.Hour)))
}
