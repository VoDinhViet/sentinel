package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"charm.land/lipgloss/v2"

	"sentinel/internal/schedule"
)

const formLabelWidth = 17

// inputStyle paints the focused input with the selection background.
func inputStyle(focused bool, text string, width int) string {
	if !focused {
		return text + strings.Repeat(" ", max(width-lipgloss.Width(text), 0))
	}
	return selectedStyle.Render(text + strings.Repeat(" ", max(width-lipgloss.Width(text), 0)))
}

func (m manageModel) renderForm() []string {
	screenLines := []string{titleStyle.Render(m.form.title), ""}
	problems := m.form.shownProblems()
	valid := len(m.form.problems()) == 0
	valueColumn := 2 + formLabelWidth + 1
	for _, index := range m.form.visible() {
		field := m.form.fields[index]
		focused := index == m.form.focus
		marker := "  "
		if focused {
			marker = lipglossAccent("› ")
		}
		label := fmt.Sprintf("%-*s", formLabelWidth, field.label)
		if _, bad := problems[field.key]; bad {
			label = severityStyle(sevBad).Render(label)
		}
		screenRow := len(screenLines)
		var value string
		switch field.kind {
		case fieldChoice:
			value = inputStyle(focused, "‹ "+field.value+" ›", 0)
			if !field.fixed {
				m.hits.add(screenRow, valueColumn, 1, fmt.Sprintf("step:%d:-1", index))
				m.hits.add(screenRow, valueColumn+lipgloss.Width(field.value)+3, 1, fmt.Sprintf("step:%d:1", index))
			}
			m.hits.add(screenRow, 0, valueColumn, fmt.Sprintf("focus:%d", index))
		case fieldChecklist:
			column := valueColumn
			var boxes []string
			for i, choice := range field.choices {
				box := "[ ] " + choice
				if field.ticked[i] {
					box = "[x] " + choice
				}
				boxes = append(boxes, inputStyle(focused && field.sub == i, box, 0))
				m.hits.add(screenRow, column, lipgloss.Width(box), fmt.Sprintf("toggle:%d:%d", index, i))
				column += lipgloss.Width(box) + 2
			}
			value = strings.Join(boxes, "  ")
			if len(field.choices) == 0 {
				value = dimStyle.Render("no repository in the config yet")
			} else if inherited := m.form.defaults.repositories; len(inherited) > 0 && !slicesAny(field.ticked) {
				value += dimStyle.Render("   default: " + strings.Join(inherited, ", "))
			}
			m.hits.add(screenRow, 0, valueColumn, fmt.Sprintf("focus:%d", index))
		case fieldRetention:
			column := valueColumn
			var boxes []string
			for i, name := range retentionKeys {
				current := focused && field.sub == i
				box := inputStyle(current, fmt.Sprintf("%4s", field.counts[i]), 4)
				if field.counts[i] == "" && !current {
					box = dimStyle.Render(fmt.Sprintf("%4s", m.retentionPlaceholder(i)))
				}
				boxes = append(boxes, dimStyle.Render(name+" ")+box)
				width := lipgloss.Width(name) + 1 + 4
				m.hits.add(screenRow, column, width, fmt.Sprintf("focus:%d:%d", index, i))
				column += width + 2
			}
			value = strings.Join(boxes, "  ")
			m.hits.add(screenRow, 0, valueColumn, fmt.Sprintf("focus:%d", index))
		default: // text, number, schedule
			value = field.value
			if focused {
				value += lipglossAccent("▏")
			}
			if field.value == "" && field.placeholder != "" {
				value += dimStyle.Render(field.placeholder)
			}
			m.hits.add(screenRow, 0, m.width, fmt.Sprintf("focus:%d", index))
		}
		screenLines = append(screenLines, marker+label+" "+value)
		if message, bad := problems[field.key]; bad {
			screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+severityStyle(sevBad).Render("✗ "+truncate(message, max(m.width-formLabelWidth-6, 10))))
		} else if field.key == "path" && m.form.field("kind").value == "local" {
			if note := folderNote(field.value); note != "" {
				screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+note)
			}
		} else if field.kind == fieldPaths {
			if note := pathsNote(field.value); note != "" {
				screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+note)
			}
		} else if field.kind == fieldSchedule {
			if preview := m.schedulePreview(field.value); preview != "" {
				screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+dimStyle.Render(preview))
			}
		}
		if focused && len(field.options) > 0 {
			screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+dimStyle.Render(truncate(strings.Join(field.options, "  "), max(m.width-formLabelWidth-5, 10))))
		}
		if field.kind == fieldPaths && focused {
			screenLines = append(screenLines, m.suggestionLines(len(screenLines), index, field.value)...)
		}
	}
	screenLines = append(screenLines, "", dimStyle.Render(wrap(m.form.fields[m.form.focus].hint, m.width-2)))
	if m.form.problem != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(wrap(m.form.problem, m.width-2)))
	}
	saveButton := "[ Save ]"
	saveStyle := keyStyle
	if !valid {
		saveStyle = dimStyle
	}
	buttons := saveStyle.Render(saveButton) + "  " + keyStyle.Render("[ Cancel ]")
	buttonRow := len(screenLines) + 1
	m.hits.add(buttonRow, 0, lipgloss.Width(saveButton), "ctrl+s")
	m.hits.add(buttonRow, lipgloss.Width(saveButton)+2, lipgloss.Width("[ Cancel ]"), "esc")
	screenLines = append(screenLines, "", buttons)
	tabHint := "next"
	if m.form.fields[m.form.focus].completes() {
		tabHint = "complete"
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "tab", tabHint, "space", "tick", "ctrl+s", "save", "esc", "cancel"))
}

// folderNote says whether the folder chosen for the backups exists, whether it can be written to
// and how much room the disk it is on has; a folder that does not exist yet is judged by the
// nearest folder above it that does.
func folderNote(folder string) string {
	folder = strings.TrimSpace(os.ExpandEnv(folder))
	if folder == "" {
		return ""
	}
	existing, created := folder, false
	var fileInfo os.FileInfo
	for {
		stat, err := os.Stat(existing)
		if err == nil && stat.IsDir() {
			fileInfo = stat
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return severityStyle(sevWarn).Render("▲ no such place")
		}
		existing, created = parent, true
	}
	var disk syscall.Statfs_t
	free := ""
	if syscall.Statfs(existing, &disk) == nil {
		free = " · " + humanBytes(int64(disk.Bavail)*int64(disk.Bsize)) + " free on this disk"
	}
	writable := canWrite(fileInfo)
	switch {
	case !writable:
		return severityStyle(sevBad).Render("✗ " + existing + " is not writable by you" + free)
	case created:
		return severityStyle(sevOK).Render("✓ will be created (" + existing + " exists)" + free)
	}
	return severityStyle(sevOK).Render("✓ exists, writable" + free)
}

// canWrite guesses from the owner, group and mode whether the current user can create files in a folder.
func canWrite(fileInfo os.FileInfo) bool {
	mode := fileInfo.Mode().Perm()
	stat, ok := fileInfo.Sys().(*syscall.Stat_t)
	switch {
	case os.Geteuid() == 0 || !ok:
		return true
	case int(stat.Uid) == os.Geteuid():
		return mode&0o200 != 0
	}
	if groups, err := os.Getgroups(); err == nil && slices.Contains(groups, int(stat.Gid)) {
		return mode&0o020 != 0
	}
	return mode&0o002 != 0
}

// suggestionLines are the lines under a focused Paths field: the folders worth adding and the
// [ Browse ] chip, each a click target. On a narrow screen the chip gets a line of its own.
// row is the first line's row within the form's lines.
func (m manageModel) suggestionLines(screenRow, index int, value string) []string {
	const browse = "[ Browse ]"
	indent := 2 + formLabelWidth + 1
	pad := strings.Repeat(" ", indent)
	wide := m.width >= 80
	line, column := pad, indent
	if wide {
		m.hits.add(screenRow, indent, len(browse), "browse")
		line, column = line+keyStyle.Render(browse)+"  ", column+len(browse)+2
	}
	suggestions := suggestFolders(splitList(value))
	if len(suggestions) > 0 {
		line, column = line+dimStyle.Render("add: "), column+len("add: ")
	}
	for number, folder := range suggestions {
		if number == 0 { // a long first folder is shortened rather than left out
			folder = truncate(folder, max(m.width-column-3, 8))
		}
		chip := fmt.Sprintf("%d %s", number+1, folder)
		if column+lipgloss.Width(chip) > m.width {
			break
		}
		m.hits.add(screenRow, column, lipgloss.Width(chip), fmt.Sprintf("suggest:%d:%d", index, number+1))
		line += keyStyle.Render(strconv.Itoa(number+1)) + dimStyle.Render(" "+folder) + "  "
		column += lipgloss.Width(chip) + 2
	}
	if wide {
		return []string{line}
	}
	var lines []string
	if len(suggestions) > 0 {
		lines = append(lines, line)
	}
	m.hits.add(screenRow+len(lines), indent, len(browse), "browse")
	return append(lines, pad+keyStyle.Render(browse))
}

// pathsNote says whether the listed paths exist on this machine.
func pathsNote(pathsText string) string {
	var missing []string
	paths := splitList(pathsText)
	for _, path := range paths {
		if _, err := os.Stat(os.ExpandEnv(path)); err != nil {
			missing = append(missing, path)
		}
	}
	switch {
	case len(paths) == 0:
		return ""
	case len(missing) == 0:
		return severityStyle(sevOK).Render(fmt.Sprintf("✓ found on this machine (%d)", len(paths)))
	}
	return severityStyle(sevWarn).Render("▲ not found here: " + strings.Join(missing, ", "))
}

func slicesAny(flags []bool) bool {
	for _, flag := range flags {
		if flag {
			return true
		}
	}
	return false
}

// retentionPlaceholder is the default count shown on an empty retention input.
func (m manageModel) retentionPlaceholder(part int) string {
	retention := m.form.defaults.retention
	count := []int{retention.Last, retention.Daily, retention.Weekly, retention.Monthly, retention.Yearly}[part]
	if count == 0 {
		return ""
	}
	return strconv.Itoa(count)
}

// schedulePreview says in words when the schedule runs next, or why it does not.
func (m manageModel) schedulePreview(spec string) string {
	if strings.TrimSpace(spec) == "" {
		spec = m.form.defaults.schedule
	}
	if schedule.IsManual(spec) {
		return "→ only run by hand"
	}
	next, ok := schedule.Next(spec, time.Now())
	if !ok {
		return ""
	}
	return fmt.Sprintf("→ %s · next %s (%s)", schedule.Describe(spec), next.Local().Format("Mon 02 Jan 15:04"), inFuture(time.Until(next)))
}

// inFuture is a rough "in 13h" for a duration ahead.
func inFuture(dur time.Duration) string {
	switch {
	case dur < time.Minute:
		return "in under a minute"
	case dur < time.Hour:
		return fmt.Sprintf("in %dm", int(dur.Minutes()))
	case dur < 48*time.Hour:
		return fmt.Sprintf("in %dh", int(dur.Hours()))
	}
	return fmt.Sprintf("in %dd", int(dur.Hours()/24))
}
