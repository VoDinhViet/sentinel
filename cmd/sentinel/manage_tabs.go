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
func (m manageModel) summary() string {
	var healthy, warning, failed, neverRun int
	for _, line := range m.tabs()[0].lines {
		switch {
		case line.cells[3] == "never run":
			neverRun++
		case line.severity == sevBad:
			failed++
		case line.severity == sevWarn:
			warning++
		default:
			healthy++
		}
	}
	checksDown := 0
	for _, check := range m.report.Checks {
		if !check.OK {
			checksDown++
		}
	}
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
