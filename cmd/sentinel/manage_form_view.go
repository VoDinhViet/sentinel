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

// railWidth is what the left rail of a grouped form takes: a vertical bar and a space.
const railWidth = 2

// headerMark starts the lines of a grouped form that are drawn without the rail: the headings
// of the sections and the advanced toggle.
const headerMark = "\x00"

func (m manageModel) renderForm() []string {
	screenLines := []string{titleStyle.Render(m.form.title), ""}
	problems := m.form.shownProblems()
	valid := len(m.form.problems()) == 0
	grouped := m.form.kind == formJob
	if grouped {
		m.width -= railWidth // the rail takes two columns of every field line
	}
	hitStart := len(m.hits.safeTargets())
	bodyStart := len(screenLines)
	valueColumn := 2 + formLabelWidth + 1
	section := ""
	for _, index := range m.form.visible() {
		field := m.form.fields[index]
		focused := index == m.form.focus
		marker := "  "
		if focused {
			marker = lipglossAccent("› ")
		}
		if grouped && field.section != section {
			section = field.section
			if len(screenLines) > bodyStart { // a blank line between sections
				screenLines = append(screenLines, headerMark)
			}
			if section != sectionAdvanced {
				screenLines = append(screenLines, headerMark+lipglossStyleAccent.Bold(true).Render(section)+" "+borderStyle.Render(strings.Repeat("─", max(m.width+railWidth-lipgloss.Width(section)-1, 0))))
			}
		}
		if field.kind == fieldToggle {
			arrow := "▸"
			if m.form.advancedOpen {
				arrow = "▾"
			}
			m.hits.add(len(screenLines), 0, m.width, fmt.Sprintf("toggle:%d:0", index))
			screenLines = append(screenLines, headerMark+marker+lipglossStyleAccent.Render(arrow+" ")+titleStyle.Render(field.label))
			continue
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
				value = dimStyle.Render("chưa có repository nào trong config")
			} else if inherited := m.form.defaults.repositories; len(inherited) > 0 && !slicesAny(field.ticked) {
				value += dimStyle.Render("   mặc định: " + strings.Join(inherited, ", "))
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
		} else if field.key == "store_at" || (field.key == "path" && m.form.field("kind").value == "local") {
			folder, prefix := field.value, ""
			if job, _ := m.form.job(); field.key == "store_at" && strings.TrimSpace(folder) == "" {
				if folder = m.form.autoFolder(job); folder != "" {
					prefix = dimStyle.Render("→ tự động: " + folder + "  ")
				}
			}
			if note := folderNote(folder); note != "" {
				note = prefix + note
				screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+note)
			}
		} else if field.kind == fieldPaths {
			if note := pathsNote(field.value); note != "" {
				screenLines = append(screenLines, "  "+strings.Repeat(" ", formLabelWidth+1)+note)
			}
		} else if field.kind == fieldSchedule {
			for _, preview := range m.schedulePreview(field.value) {
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
	if grouped {
		rail := borderStyle.Render("│") + " "
		for i := bodyStart; i < len(screenLines); i++ {
			if text, isHeader := strings.CutPrefix(screenLines[i], headerMark); isHeader {
				screenLines[i] = text
			} else {
				screenLines[i] = rail + text
			}
		}
		m.hits.shift(hitStart, railWidth)
		m.width += railWidth
	}
	screenLines = append(screenLines, "", dimStyle.Render(wrap(m.form.fields[m.form.focus].hint, m.width-2)))
	if m.form.problem != "" {
		screenLines = append(screenLines, "", severityStyle(sevBad).Render(wrap(m.form.problem, m.width-2)))
	}
	saveButton := "[ Lưu ]"
	saveStyle := keyStyle
	if !valid {
		saveStyle = dimStyle
	}
	cancelButton := "[ Huỷ ]"
	buttons := saveStyle.Render(saveButton) + "  " + keyStyle.Render(cancelButton)
	buttonRow := len(screenLines) + 1
	m.hits.add(buttonRow, 0, lipgloss.Width(saveButton), "ctrl+s")
	m.hits.add(buttonRow, lipgloss.Width(saveButton)+2, lipgloss.Width(cancelButton), "esc")
	screenLines = append(screenLines, "", buttons)
	tabHint := "tiếp"
	if m.form.fields[m.form.focus].completes() {
		tabHint = "hoàn thành"
	}
	return append(screenLines, "", m.keyBar(len(screenLines)+1, "tab", tabHint, "space", "đánh dấu", "ctrl+s", "lưu", "esc", "huỷ"))
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
			return severityStyle(sevWarn).Render("▲ không thấy thư mục này")
		}
		existing, created = parent, true
	}
	var disk syscall.Statfs_t
	free := ""
	if syscall.Statfs(existing, &disk) == nil {
		free = " · còn trống " + humanBytes(int64(disk.Bavail)*int64(disk.Bsize)) + " trên ổ đĩa này"
	}
	writable := canWrite(fileInfo)
	switch {
	case !writable:
		return severityStyle(sevBad).Render("✗ " + existing + " bạn không có quyền ghi" + free)
	case created:
		return severityStyle(sevOK).Render("✓ sẽ được tạo (" + existing + " đã có)" + free)
	}
	return severityStyle(sevOK).Render("✓ đã có, ghi được" + free)
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
	const browse = "[ Duyệt ]"
	indent := 2 + formLabelWidth + 1
	pad := strings.Repeat(" ", indent)
	wide := m.width >= 80
	line, column := pad, indent
	if wide {
		m.hits.add(screenRow, indent, lipgloss.Width(browse), "browse")
		line, column = line+keyStyle.Render(browse)+"  ", column+lipgloss.Width(browse)+2
	}
	suggestions := suggestFolders(splitList(value))
	const addLabel = "thêm: "
	if len(suggestions) > 0 {
		line, column = line+dimStyle.Render(addLabel), column+lipgloss.Width(addLabel)
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
	m.hits.add(screenRow+len(lines), indent, lipgloss.Width(browse), "browse")
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
		return severityStyle(sevOK).Render(fmt.Sprintf("✓ có trên máy này (%d)", len(paths)))
	}
	return severityStyle(sevWarn).Render("▲ không thấy ở đây: " + strings.Join(missing, ", "))
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
func (m manageModel) schedulePreview(spec string) []string {
	if strings.TrimSpace(spec) == "" {
		spec = m.form.defaults.schedule
	}
	if schedule.IsManual(spec) {
		return []string{"→ chỉ chạy thủ công"}
	}
	runs := schedule.NextRuns(spec, time.Now(), 3)
	if len(runs) == 0 {
		return nil
	}
	room := max(m.width-formLabelWidth-5, 20)
	lines := []string{truncate(fmt.Sprintf("→ %s · lần tới %s (%s)", schedule.Describe(spec), runDay(runs[0]), inFuture(time.Until(runs[0]))), room)}
	var later []string
	for _, run := range runs[1:] {
		layout := "02/01"
		if run.Local().Format("15:04") != runs[0].Local().Format("15:04") { // every 6h and the like
			layout = "02/01 15:04"
		}
		later = append(later, run.Local().Format(layout))
	}
	return append(lines, truncate("  sau đó "+strings.Join(later, ", "), room))
}

// weekdayNames are the short Vietnamese weekdays, indexed like time.Weekday (Sunday first).
var weekdayNames = [7]string{"CN", "T2", "T3", "T4", "T5", "T6", "T7"}

// runDay is a run time as "T2 02/01 15:04".
func runDay(run time.Time) string {
	local := run.Local()
	return weekdayNames[local.Weekday()] + " " + local.Format("02/01 15:04")
}

// inFuture is a rough "trong 13 giờ" for a duration ahead.
func inFuture(dur time.Duration) string {
	switch {
	case dur < time.Minute:
		return "trong chưa đầy một phút"
	case dur < time.Hour:
		return fmt.Sprintf("trong %d phút", int(dur.Minutes()))
	case dur < 48*time.Hour:
		return fmt.Sprintf("trong %d giờ", int(dur.Hours()))
	}
	return fmt.Sprintf("trong %d ngày", int(dur.Hours()/24))
}
