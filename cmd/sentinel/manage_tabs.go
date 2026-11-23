package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"sentinel/internal/configedit"
	"sentinel/internal/schedule"
)

// The tabs of the manager; doctorTab and configTab are declared next to their code.
const (
	backupsTab = iota
	uptimeTab
	alertsTab
)

// tabs turns the report into the five manager tabs.
func (m manageModel) tabs() []manageTab {
	now := m.report.GeneratedAt
	// Columns are ordered by importance: a narrow screen drops the last ones.
	backups := manageTab{title: "Backups", headers: []string{"JOB", "REPOSITORY", "LẦN CUỐI", "KẾT QUẢ", "LỊCH SỬ", "SNAPSHOT", "THÊM", "TỔNG", "LẦN TỚI"}, statusCol: 3, historyCol: 4}
	for _, backup := range m.report.Jobs {
		nextRun, _ := m.nextRun(backup.Job)
		line := manageLine{
			cells:   []string{backup.Job, backup.Repository, relativeAgo(backup.FinishedAt, now), "ok", "", shortID(backup.SnapshotID), humanBytes(backup.BytesAdded), humanBytes(backup.TotalBytes), nextRun},
			detail:  backup.Job + " → " + backup.Repository + ": " + backup.Result,
			job:     backup.Job,
			history: backup.Recent, repository: backup.Repository,
		}
		switch {
		case !backup.Success:
			line.severity, line.cells[3], line.cells[5], line.cells[6], line.cells[7] = sevBad, "LỖI", "", "", ""
			line.sub = firstLine(backup.Error)
		case backup.Warning != "":
			line.severity, line.cells[3] = sevWarn, "cảnh báo"
			line.sub = firstLine(backup.Warning)
		}
		line.fields = m.backupFields(line, backup.SnapshotID, backup.FinishedAt)
		if running, ok := m.runningFor(backup.Job, backup.Repository); ok {
			line.severity, line.cells[3], line.detail, line.running, line.sub = sevRun, runningResult(running), runningDetail(running, now), true, runningBrief(running, now)
		}
		backups.lines = append(backups.lines, line)
	}
	for _, jobRepo := range m.jobRepositories {
		if !m.hasBackupLine(jobRepo) {
			nextRun, _ := m.nextRun(jobRepo.job)
			line := manageLine{
				cells:    []string{jobRepo.job, jobRepo.repository, "chưa có", cellNeverRun, "", "", "", "", nextRun},
				severity: sevWarn,
				detail:   jobRepo.job + " → " + jobRepo.repository + ": chưa chạy lần nào. Bấm b để backup ngay.",
				job:      jobRepo.job, repository: jobRepo.repository,
			}
			line.fields = m.backupFields(line, "", time.Time{})
			if running, ok := m.runningFor(jobRepo.job, jobRepo.repository); ok {
				line.severity, line.cells[3], line.detail, line.running, line.sub = sevRun, runningResult(running), runningDetail(running, now), true, runningBrief(running, now)
			}
			backups.lines = append(backups.lines, line)
		}
	}
	backups.count, backups.alarm = len(backups.lines), hasSeverity(backups.lines, sevBad)
	checks := manageTab{title: "Uptime", headers: []string{"CHECK", "TRẠNG THÁI", "ĐỘ TRỄ", "LẦN CUỐI"}, statusCol: 1, historyCol: -1}
	for _, check := range m.report.Checks {
		line := manageLine{
			cells:  []string{check.Name, "UP", (time.Duration(check.LatencyMs) * time.Millisecond).String(), relativeAgo(check.At, now)},
			detail: check.Name + ": up",
		}
		if !check.OK {
			line.severity, line.cells[1], line.detail, line.sub = sevBad, "DOWN", check.Name+": "+check.Error, firstLine(check.Error)
		}
		checks.lines = append(checks.lines, line)
	}
	checks.count, checks.alarm = len(checks.lines), hasSeverity(checks.lines, sevBad)
	alerts := manageTab{title: "Alerts", headers: []string{"ALERT", "TỪ", "NỘI DUNG"}, statusCol: -1, historyCol: -1}
	for _, alert := range m.report.Alerts {
		alerts.lines = append(alerts.lines, manageLine{
			cells:    []string{alert.Key, relativeAgo(alert.Since, now), alert.Message},
			severity: sevBad,
			detail:   alert.Key + ": " + alert.Message,
		})
	}
	alerts.count, alerts.alarm = len(alerts.lines), len(alerts.lines) > 0
	return []manageTab{backups, checks, alerts, m.buildDoctorTab(), m.buildConfigTab()}
}

// hasSeverity says some line has that severity.
func hasSeverity(lines []manageLine, level severity) bool {
	return slices.ContainsFunc(lines, func(line manageLine) bool { return line.severity == level })
}

// firstLine is the first line of a multi-line message, so one row can show it.
func firstLine(message string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(first)
}

// configuredJob is the job of that name in the config file, when the file has been read.
func (m manageModel) configuredJob(name string) (configedit.Job, bool) {
	for _, job := range m.config.listing.Jobs {
		if job.Name == name {
			return job, true
		}
	}
	return configedit.Job{}, false
}

// scheduleOf is the schedule a job runs on: its own, or the default of the config.
func (m manageModel) scheduleOf(job configedit.Job) string {
	return orDefault(job.Schedule, m.config.overview.Settings.DefaultSchedule)
}

// nextRun is when a job runs next, e.g. "T3 24/11 02:00"; "thủ công" for a job that only runs
// by hand, and "" when the config is not known.
func (m manageModel) nextRun(name string) (text string, known bool) {
	job, ok := m.configuredJob(name)
	if !ok {
		return "", false
	}
	spec := m.scheduleOf(job)
	if spec == "" || schedule.IsManual(spec) {
		return "thủ công", true
	}
	if runs := schedule.NextRuns(spec, m.report.GeneratedAt, 1); len(runs) > 0 {
		return runDay(runs[0]), true
	}
	return "", false
}

// backupFields are the label/value pairs the detail panel shows for a line of the Backups tab.
func (m manageModel) backupFields(line manageLine, snapshotID string, finishedAt time.Time) [][2]string {
	fields := [][2]string{{"Repository", line.repository}}
	if job, ok := m.configuredJob(line.job); ok {
		keep := job.Retention.String()
		if job.Inherited.Retention {
			keep = m.config.overview.Settings.DefaultRetention.String() + " (mặc định)"
		}
		fields = m.jobSetupFields(job, line.repository, keep)
	}
	if snapshotID != "" {
		fields = append(fields, [2]string{"Snapshot", shortID(snapshotID) + " · " + relativeAgo(finishedAt, m.report.GeneratedAt)})
	}
	return fields
}

// cellNeverRun is the result cell of a job that has not run yet; the counts recognise it by it.
const cellNeverRun = "chưa chạy"

// healthCounts is how the jobs, alerts and checks add up, for the summary line and the overview panel.
type healthCounts struct{ healthy, warning, failed, neverRun, checksDown, alerts int }

func (m manageModel) healthCounts() healthCounts {
	var counts healthCounts
	for _, line := range m.tabs()[backupsTab].lines {
		switch {
		case line.cells[3] == cellNeverRun:
			counts.neverRun++
		case line.severity == sevBad:
			counts.failed++
		case line.severity == sevWarn:
			counts.warning++
		default:
			counts.healthy++
		}
	}
	for _, check := range m.report.Checks {
		if !check.OK {
			counts.checksDown++
		}
	}
	counts.alerts = len(m.report.Alerts)
	return counts
}

// summary is one line of what needs attention, or "all good": the header of a screen too small
// for the panels.
func (m manageModel) summary() string {
	counts := m.healthCounts()
	healthy, warning, failed, neverRun, checksDown := counts.healthy, counts.warning, counts.failed, counts.neverRun, counts.checksDown
	type part struct {
		text  string
		level severity
	}
	var parts []part
	add := func(count int, label string, level severity) {
		if count > 0 {
			parts = append(parts, part{fmt.Sprintf("%d %s", count, label), level})
		}
	}
	add(failed, "lỗi", sevBad)
	add(len(m.report.Alerts), "alert", sevBad)
	add(checksDown, "check down", sevBad)
	add(warning, "cảnh báo", sevWarn)
	add(neverRun, "chưa chạy", sevWarn)
	add(healthy, "ổn", sevOK)
	if len(parts) == 0 {
		return dimStyle.Render("chưa cấu hình gì")
	}
	// On a narrow screen the least important parts (the last) are dropped.
	const separator = " · "
	for len(parts) > 1 {
		total := lipgloss.Width(separator) * (len(parts) - 1)
		for _, part := range parts {
			total += lipgloss.Width(part.text)
		}
		if total <= m.width {
			break
		}
		parts = parts[:len(parts)-1]
	}
	rendered := make([]string, len(parts))
	for i, part := range parts {
		rendered[i] = severityStyle(part.level).Render(part.text)
	}
	return strings.Join(rendered, dimStyle.Render(separator))
}

func (m manageModel) hasBackupLine(jobRepo jobRepository) bool {
	for _, backup := range m.report.Jobs {
		if backup.Job == jobRepo.job && backup.Repository == jobRepo.repository {
			return true
		}
	}
	return false
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
