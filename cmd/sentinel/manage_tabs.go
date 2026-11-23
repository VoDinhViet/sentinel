package main

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// tabs turns the report into the three manager tabs.
func (m manageModel) tabs() []manageTab {
	now := m.report.GeneratedAt
	// Columns are ordered by importance: a narrow screen drops the last ones.
	backups := manageTab{title: "Backups", headers: []string{"JOB", "REPOSITORY", "LAST RUN", "RESULT", "HISTORY", "SNAPSHOT", "ADDED"}, statusCol: 3, historyCol: 4}
	for _, backup := range m.report.Jobs {
		line := manageLine{
			cells:   []string{backup.Job, backup.Repository, relativeAgo(backup.FinishedAt, now), "ok", "", shortID(backup.SnapshotID), humanBytes(backup.BytesAdded)},
			detail:  backup.Job + " → " + backup.Repository + ": " + backup.Result,
			job:     backup.Job,
			history: backup.Recent, repository: backup.Repository,
		}
		switch {
		case !backup.Success:
			line.severity, line.cells[3], line.cells[5], line.cells[6] = sevBad, "FAILED", "", ""
		case backup.Warning != "":
			line.severity, line.cells[3] = sevWarn, "warning"
		}
		backups.lines = append(backups.lines, line)
	}
	for _, jobRepo := range m.jobRepositories {
		if !m.hasBackupLine(jobRepo) {
			backups.lines = append(backups.lines, manageLine{
				cells:    []string{jobRepo.job, jobRepo.repository, "never", "never run", "", "", ""},
				severity: sevWarn,
				detail:   jobRepo.job + " → " + jobRepo.repository + ": never run. Press b to back it up now.",
				job:      jobRepo.job, repository: jobRepo.repository,
			})
		}
	}
	checks := manageTab{title: "Uptime", headers: []string{"CHECK", "STATUS", "LATENCY", "LAST CHECK"}, statusCol: 1, historyCol: -1}
	for _, check := range m.report.Checks {
		line := manageLine{
			cells:  []string{check.Name, "UP", (time.Duration(check.LatencyMs) * time.Millisecond).String(), relativeAgo(check.At, now)},
			detail: check.Name + ": up",
		}
		if !check.OK {
			line.severity, line.cells[1], line.detail = sevBad, "DOWN", check.Name+": "+check.Error
		}
		checks.lines = append(checks.lines, line)
	}
	alerts := manageTab{title: "Alerts", headers: []string{"ALERT", "SINCE", "MESSAGE"}, statusCol: -1, historyCol: -1}
	for _, alert := range m.report.Alerts {
		alerts.lines = append(alerts.lines, manageLine{
			cells:    []string{alert.Key, relativeAgo(alert.Since, now), alert.Message},
			severity: sevBad,
			detail:   alert.Key + ": " + alert.Message,
		})
	}
	return []manageTab{backups, checks, alerts, m.buildDoctorTab(), m.buildConfigTab()}
}

// summary is the one line under the title: what needs attention, or "all good".
// healthCounts is how the jobs, alerts and checks add up, for the summary line and the cards.
type healthCounts struct{ healthy, warning, failed, neverRun, checksDown, alerts int }

func (m manageModel) healthCounts() healthCounts {
	var counts healthCounts
	for _, line := range m.tabs()[0].lines {
		switch {
		case line.cells[3] == "never run":
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

// cardsMinWidth is the screen width from which the summary becomes a row of cards.
const cardsMinWidth = 110

// showCards says whether the header is a row of cards (three lines) instead of the summary line.
func (m manageModel) showCards() bool {
	counts := m.healthCounts()
	return m.mode == modeTable && m.loaded && m.width >= cardsMinWidth &&
		counts.healthy+counts.warning+counts.failed+counts.neverRun+counts.checksDown+counts.alerts > 0
}

// renderCards is the summary as five small boxes (healthy, failed, warnings, alerts, checks
// down); a box with a zero is drawn quietly.
func (m manageModel) renderCards() []string {
	counts := m.healthCounts()
	type card struct {
		count int
		label string
		level severity
	}
	cards := []card{
		{counts.healthy, "healthy", sevOK},
		{counts.failed, "failed", sevBad},
		{counts.warning + counts.neverRun, "warnings", sevWarn},
		{counts.alerts, plural(counts.alerts, "alert", "alerts"), sevBad},
		{counts.checksDown, plural(counts.checksDown, "check down", "checks down"), sevBad},
	}
	const gap = 2
	width := min((m.width-gap*(len(cards)-1))/len(cards), 26)
	var tops, middles, bottoms []string
	for _, c := range cards {
		border := borderStyle
		number := dimStyle
		if c.count > 0 {
			border = lipgloss.NewStyle().Foreground(severityStyle(c.level).GetForeground())
			number = severityStyle(c.level)
		}
		text := number.Render(fmt.Sprintf("%d", c.count)) + " " + dimStyle.Render(c.label)
		pad := max(width-2-2-lipgloss.Width(text), 0)
		tops = append(tops, border.Render("╭"+strings.Repeat("─", width-2)+"╮"))
		middles = append(middles, border.Render("│")+" "+text+strings.Repeat(" ", pad)+" "+border.Render("│"))
		bottoms = append(bottoms, border.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	}
	space := strings.Repeat(" ", gap)
	return []string{strings.Join(tops, space), strings.Join(middles, space), strings.Join(bottoms, space)}
}

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
	add(failed, "failed", sevBad)
	add(len(m.report.Alerts), plural(len(m.report.Alerts), "alert", "alerts"), sevBad)
	add(checksDown, plural(checksDown, "check down", "checks down"), sevBad)
	add(warning, plural(warning, "warning", "warnings"), sevWarn)
	add(neverRun, "never run", sevWarn)
	add(healthy, "healthy", sevOK)
	if len(parts) == 0 {
		return dimStyle.Render("nothing configured yet")
	}
	// On a narrow screen the least important parts (the last) are dropped.
	const separator = " · "
	for len(parts) > 1 {
		total := len(separator) * (len(parts) - 1)
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

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
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
