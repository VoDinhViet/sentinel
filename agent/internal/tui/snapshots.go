package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/restic"
)

// snapshotsScreen lists the snapshots of one job in one repository, newest first, to pick one to restore.
type snapshotsScreen struct {
	job, repo string
	snaps     []restic.Snapshot
	cursor    int
}

// afterSnapshots opens the list when the listing is back, or says why it failed.
func (m manageModel) afterSnapshots(msg snapshotsMsg) manageModel {
	m.endBusy()
	if msg.err != nil {
		m.notice, m.noticeBad = fmt.Sprintf("%s → %s: %v", msg.job, msg.repository, msg.err), true
		return m
	}
	return m.push(snapshotsScreen{job: msg.job, repo: msg.repository, snaps: msg.snapshots})
}

func (s snapshotsScreen) onKey(m manageModel, key, _ string) (manageModel, tea.Cmd) {
	switch key {
	case "?":
		return m.push(snapshotsHelp()), nil
	case "esc", "q":
		return m.pop(), nil
	case "down", "j":
		s.cursor = min(s.cursor+1, max(len(s.snaps)-1, 0))
	case "up", "k":
		s.cursor = max(s.cursor-1, 0)
	case "g", "home":
		s.cursor = 0
	case "G", "end":
		s.cursor = max(len(s.snaps)-1, 0)
	case "enter", "r":
		if len(s.snaps) == 0 {
			return m, nil
		}
		return m.push(targetScreen{job: s.job, repo: s.repo, snap: s.snaps[s.cursor]}), nil
	}
	return m.replaceTop(s), nil
}

// click selects a snapshot; a second click on the selected one starts its restore.
func (s snapshotsScreen) click(m manageModel, index int) (manageModel, tea.Cmd) {
	if index == s.cursor {
		return s.onKey(m, "enter", "")
	}
	s.cursor = index
	return m.replaceTop(s), nil
}

// scroll moves the selection by step lines, staying in the list.
func (s snapshotsScreen) scroll(m manageModel, step int) manageModel {
	s.cursor = min(max(s.cursor+step, 0), max(len(s.snaps)-1, 0))
	return m.replaceTop(s)
}

func (s snapshotsScreen) render(m manageModel) []string {
	screenLines := []string{titleStyle.Render(fmt.Sprintf("Snapshots của %s trong %s", s.job, s.repo)), ""}
	if len(s.snaps) == 0 {
		return append(screenLines, dimStyle.Render("Chưa có snapshot nào của job này trong repository này."), "", m.keyBar(len(screenLines)+2, "esc", "quay lại"))
	}
	visible := max(m.height-8, 3)
	first := firstVisible(s.cursor, visible)
	screenLines = append(screenLines, "  "+headerStyle.Render(fmt.Sprintf("%-10s %-17s %s", "SNAPSHOT", "THỜI GIAN", "KÍCH THƯỚC")))
	for i := first; i < min(first+visible, len(s.snaps)); i++ {
		snap := s.snaps[i]
		size := ""
		if snap.Summary != nil {
			size = backup.HumanBytes(snap.Summary.TotalBytes)
		}
		line := fmt.Sprintf("%-10s %-17s %s", shortID(snap.ID), snap.Time.Local().Format("2006-01-02 15:04"), size)
		m.hits.add(len(screenLines), 0, m.width, fmt.Sprintf("snap:%d", i))
		if i == s.cursor {
			text := "› " + line
			screenLines = append(screenLines, selectedStyle.Render(text+strings.Repeat(" ", max(m.width-lipgloss.Width(text), 0))))
		} else {
			screenLines = append(screenLines, "  "+line)
		}
	}
	if hidden := len(s.snaps) - (first + visible); hidden > 0 {
		screenLines = append(screenLines, dimStyle.Render(fmt.Sprintf("  … còn %d", hidden)))
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "↑↓", "chọn", "enter", "restore…", "esc", "quay lại"))
}
