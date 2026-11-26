package tui

import (
	"fmt"
	"time"

	"sentinel/internal/backup"
	"sentinel/internal/status"
)

// runningFor is the backup in progress for a job and repository, if there is one.
func (m manageModel) runningFor(job, repository string) (status.Running, bool) {
	for _, running := range m.report.Running {
		if running.Job == job && running.Repository == repository {
			return running, true
		}
	}
	return status.Running{}, false
}

// runningResult is the short text for the RESULT cell of a backup in progress: "⏳ ~37%".
func runningResult(running status.Running) string {
	switch {
	case running.Phase == backup.PhaseRetention:
		return "⏳ retention"
	case running.Percent != nil && running.Estimated:
		return fmt.Sprintf("⏳ ~%.0f%%", *running.Percent)
	case running.Percent != nil:
		return fmt.Sprintf("⏳ %.0f%%", *running.Percent)
	}
	return "⏳ " + backup.HumanBytes(running.BytesDone)
}

// runningBrief is the short line under a running backup's row: the progress, how long it has run
// and a warning when there is one.
func runningBrief(running status.Running, now time.Time) string {
	if running.Phase == backup.PhaseRetention {
		return "đang áp dụng retention (xoá snapshot cũ)"
	}
	text := runningProgress(running) + " · đã chạy " + vietnameseDuration(now.Sub(running.StartedAt))
	if warning := runningWarning(running); warning != "" {
		text += " · " + warning
	}
	return text
}

// runningProgress is "1.2 GiB / 1.9 GiB · 12 MiB/s · còn ~25 phút".
func runningProgress(running status.Running) string {
	text := backup.HumanBytes(running.BytesDone)
	if running.BytesTotal > 0 {
		text += " / " + backup.HumanBytes(running.BytesTotal)
		if running.Estimated {
			text += " (ước lượng)"
		}
	}
	if running.BytesPerSecond > 0 {
		text += " · " + backup.HumanBytes(int64(running.BytesPerSecond)) + "/s"
	}
	if running.RemainingSeconds > 0 {
		text += " · " + timeLeftText(running.RemainingSeconds)
	}
	return text
}

// runningWarning says what is wrong with a running backup, or "" when nothing is.
func runningWarning(running status.Running) string {
	switch {
	case running.Stalled:
		return "⚠ không có dữ liệu mới hơn 10 phút"
	case running.WillExceedTimeout:
		return "⚠ sẽ vượt timeout"
	}
	return ""
}

// runningDetail is the line under the table for a backup in progress.
func runningDetail(running status.Running, now time.Time) string {
	text := running.Job + " → " + running.Repository + ": "
	if running.Phase == backup.PhaseRetention {
		return text + "đang áp dụng retention (xoá snapshot cũ). Bấm x để dừng."
	}
	text += "đang backup, "
	switch {
	case running.Percent != nil && running.Estimated:
		text += fmt.Sprintf("~%.0f%% (đã dump %s, ước lượng ~%s theo lần backup trước)", *running.Percent, backup.HumanBytes(running.BytesDone), backup.HumanBytes(running.BytesTotal))
	case running.Percent != nil:
		text += fmt.Sprintf("%.0f%% (%s / %s)", *running.Percent, backup.HumanBytes(running.BytesDone), backup.HumanBytes(running.BytesTotal))
	default:
		text += "đã xử lý " + backup.HumanBytes(running.BytesDone) + " (chưa có lần backup trước để ước lượng %)"
	}
	if running.BytesPerSecond > 0 {
		text += " · " + backup.HumanBytes(int64(running.BytesPerSecond)) + "/s"
	}
	if running.RemainingSeconds > 0 {
		text += " · " + timeLeftText(running.RemainingSeconds)
	}
	text += " · đã chạy " + vietnameseDuration(now.Sub(running.StartedAt))
	switch {
	case running.Stalled:
		text += ". ⚠ Không có dữ liệu mới hơn 10 phút: database hoặc mạng có thể đang treo"
	case running.WillExceedTimeout:
		text += fmt.Sprintf(". ⚠ Với tốc độ này sẽ vượt timeout %s của job và bị cắt: tăng timeout trong Tuỳ chọn nâng cao", vietnameseDuration(time.Duration(running.TimeoutSeconds)*time.Second))
	}
	return text + ". Bấm x để dừng; đóng manage không làm backup dừng."
}

// timeLeftText is "còn ~25 phút", or "còn <1 phút" when the estimate is under a minute.
func timeLeftText(seconds int64) string {
	left := time.Duration(seconds) * time.Second
	if left.Round(time.Minute) < time.Minute {
		return "còn <1 phút"
	}
	return "còn ~" + vietnameseDuration(left)
}

// vietnameseDuration writes a time like "25 phút" or "3 giờ 10 phút".
func vietnameseDuration(dur time.Duration) string {
	dur = dur.Round(time.Minute)
	switch {
	case dur < time.Minute:
		return "chưa tới 1 phút"
	case dur < time.Hour:
		return fmt.Sprintf("%d phút", int(dur.Minutes()))
	}
	if minutes := int(dur.Minutes()) % 60; minutes > 0 {
		return fmt.Sprintf("%d giờ %d phút", int(dur.Hours()), minutes)
	}
	return fmt.Sprintf("%d giờ", int(dur.Hours()))
}
