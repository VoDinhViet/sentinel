package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/configedit"
	"sentinel/agent/internal/schedule"
	"sentinel/agent/internal/status"
)

// The tabs of the manager; doctorTab, configTab and serverTab are declared next to their code.
const (
	backupsTab = iota
	uptimeTab
	alertsTab
)

// tabs turns the report into the seven manager tabs.
func (m manageModel) tabs() []manageTab {
	if m.fleetOnly { // the other tabs are about this machine, which has no config here
		tabs := make([]manageTab, fleetTab+1)
		tabs[fleetTab] = m.buildFleetTab()
		return tabs
	}
	return []manageTab{m.backupsTab(), checksTabOf(m.report), alertsTabOf(m.report), m.buildDoctorTab(), m.buildConfigTab(), m.buildServerTab(), m.buildFleetTab()}
}

// shownTabs are the tabs on the tab bar, in order: all of them, or the Fleet tab alone.
func (m manageModel) shownTabs() []int {
	if m.fleetOnly {
		return []int{fleetTab}
	}
	shown := make([]int, fleetTab+1)
	for i := range shown {
		shown[i] = i
	}
	return shown
}

// neighbourTab is the tab step places away from the open one (1 next, -1 previous), wrapping round.
func (m manageModel) neighbourTab(step int) int {
	shown := m.shownTabs()
	position := slices.Index(shown, m.tab)
	return shown[(position+step+len(shown))%len(shown)]
}

// backupsTab is the Backups tab of this machine: the report, with the schedules and settings of its config.
func (m manageModel) backupsTab() manageTab {
	return backupsTabOf(m.report, backupKnowledge{
		nextRun: func(job string) string {
			text, _ := m.nextRun(job)
			return text
		},
		fields:          m.backupFields,
		jobRepositories: m.jobRepositories,
	})
}

// backupKnowledge is what the Backups tab knows besides the report. A machine seen from far away
// has none of it, and the tab then shows what the report says.
type backupKnowledge struct {
	nextRun func(job string) string // when the job runs next; nil when the schedule is not known
	// fields are the label/value pairs of the detail panel; nil for the plain ones.
	fields func(line manageLine, snapshotID string, finishedAt time.Time) [][2]string
	// jobRepositories are the backups the config describes, to show the ones that never ran.
	jobRepositories []jobRepository
}

func (known backupKnowledge) nextRunOf(job string) string {
	if known.nextRun == nil {
		return ""
	}
	return known.nextRun(job)
}

func (known backupKnowledge) fieldsOf(report status.Report, line manageLine, snapshotID string, finishedAt time.Time) [][2]string {
	if known.fields != nil {
		return known.fields(line, snapshotID, finishedAt)
	}
	return plainBackupFields(line, snapshotID, finishedAt, report.GeneratedAt)
}

// plainBackupFields are the pairs the detail panel shows for a backup when only the report is known.
func plainBackupFields(line manageLine, snapshotID string, finishedAt, now time.Time) [][2]string {
	fields := [][2]string{{"Repository", line.repository}}
	if snapshotID != "" {
		fields = append(fields, [2]string{"Snapshot", shortID(snapshotID) + " · " + relativeAgo(finishedAt, now)})
	}
	return fields
}

// backupsTabOf is a line for the latest run of each job and repository, then one for each backup
// the config describes that has not run yet.
func backupsTabOf(report status.Report, known backupKnowledge) manageTab {
	tab := manageTab{title: "Backups", headers: []string{"JOB", "REPOSITORY", "LẦN CUỐI", "KẾT QUẢ", "LỊCH SỬ", "SNAPSHOT", "THÊM", "TỔNG", "LẦN TỚI"}, statusCol: 3, historyCol: 4}
	for _, latestRun := range report.Jobs {
		tab.lines = append(tab.lines, backupLineOf(report, known, latestRun))
	}
	for _, jobRepo := range known.jobRepositories {
		if !hasBackupLine(report.Jobs, jobRepo) {
			tab.lines = append(tab.lines, neverRunLineOf(report, known, jobRepo))
		}
	}
	tab.count, tab.alarm = len(tab.lines), hasSeverity(tab.lines, sevBad)
	return tab
}

// backupLineOf is the line of the latest run of one job into one repository.
func backupLineOf(report status.Report, known backupKnowledge, latestRun status.Job) manageLine {
	now := report.GeneratedAt
	line := manageLine{
		cells: []string{latestRun.Job, latestRun.Repository, relativeAgo(latestRun.FinishedAt, now), "ok", "", shortID(latestRun.SnapshotID),
			backup.HumanBytes(latestRun.BytesAdded), backup.HumanBytes(latestRun.TotalBytes), known.nextRunOf(latestRun.Job)},
		detail:  latestRun.Job + " → " + latestRun.Repository + ": " + latestRun.Result,
		job:     latestRun.Job,
		history: latestRun.Recent, repository: latestRun.Repository,
	}
	switch {
	case !latestRun.Success:
		line.severity, line.cells[3], line.cells[5], line.cells[6], line.cells[7] = sevBad, "LỖI", "", "", ""
		line.sub = firstLine(latestRun.Error)
	case latestRun.Warning != "":
		line.severity, line.cells[3] = sevWarn, "cảnh báo"
		line.sub = firstLine(latestRun.Warning)
	}
	line.fields = known.fieldsOf(report, line, latestRun.SnapshotID, latestRun.FinishedAt)
	return showRunning(report, line)
}

// neverRunLineOf is the line of a backup the config describes that has no run yet.
func neverRunLineOf(report status.Report, known backupKnowledge, jobRepo jobRepository) manageLine {
	line := manageLine{
		cells:    []string{jobRepo.job, jobRepo.repository, "chưa có", cellNeverRun, "", "", "", "", known.nextRunOf(jobRepo.job)},
		severity: sevWarn,
		detail:   jobRepo.job + " → " + jobRepo.repository + ": chưa chạy lần nào. Bấm b để backup ngay.",
		job:      jobRepo.job, repository: jobRepo.repository,
	}
	line.fields = known.fieldsOf(report, line, "", time.Time{})
	return showRunning(report, line)
}

// showRunning marks a line whose backup is in progress, with its progress in place of the result.
func showRunning(report status.Report, line manageLine) manageLine {
	if running, ok := runningIn(report, line.job, line.repository); ok {
		now := report.GeneratedAt
		line.severity, line.cells[3], line.detail, line.running, line.sub = sevRun, runningResult(running), runningDetail(running, now), true, runningBrief(running, now)
	}
	return line
}

// checksTabOf is a line for the latest result of each uptime check.
func checksTabOf(report status.Report) manageTab {
	tab := manageTab{title: "Uptime", headers: []string{"CHECK", "TRẠNG THÁI", "ĐỘ TRỄ", "LẦN CUỐI"}, statusCol: 1, historyCol: -1}
	for _, check := range report.Checks {
		line := manageLine{
			cells:  []string{check.Name, "UP", (time.Duration(check.LatencyMs) * time.Millisecond).String(), relativeAgo(check.At, report.GeneratedAt)},
			detail: check.Name + ": up",
		}
		if !check.OK {
			line.severity, line.cells[1], line.detail, line.sub = sevBad, "DOWN", check.Name+": "+check.Error, firstLine(check.Error)
		}
		tab.lines = append(tab.lines, line)
	}
	tab.count, tab.alarm = len(tab.lines), hasSeverity(tab.lines, sevBad)
	return tab
}

// alertsTabOf is a line for each open alert.
func alertsTabOf(report status.Report) manageTab {
	tab := manageTab{title: "Alerts", headers: []string{"ALERT", "TỪ", "NỘI DUNG"}, statusCol: -1, historyCol: -1}
	for _, alert := range report.Alerts {
		tab.lines = append(tab.lines, manageLine{
			cells:    []string{alert.Key, relativeAgo(alert.Since, report.GeneratedAt), alert.Message},
			severity: sevBad,
			detail:   alert.Key + ": " + alert.Message,
		})
	}
	tab.count, tab.alarm = len(tab.lines), len(tab.lines) > 0
	return tab
}

// hasBackupLine says the report has a run of that job into that repository.
func hasBackupLine(jobs []status.Job, jobRepo jobRepository) bool {
	return slices.ContainsFunc(jobs, func(latestRun status.Job) bool {
		return latestRun.Job == jobRepo.job && latestRun.Repository == jobRepo.repository
	})
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
		keep := retentionText(job.Retention)
		if job.Inherited.Retention {
			keep = retentionText(m.config.overview.Settings.DefaultRetention) + " (mặc định)"
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

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
