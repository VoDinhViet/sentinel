package main

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"sentinel/internal/backup"
	"sentinel/internal/schedule"
)

const (
	// The three panels above the tabs need this much screen.
	topPanelsMinWidth  = 110
	topPanelsMinHeight = 28
	topPanelRows       = 6 // two border lines and four lines of text
	// detailFieldRows is how many rows the label/value pairs of the detail panel take.
	detailFieldRows = 3
	// maxPanelItems is how many backups or checks the top panels list before "+N khác".
	maxPanelItems = 4
)

// renderDashboard is the main screen: a title line, the overview panels (or one summary line on a
// small screen), the tabbed table, the detail of the selected line and the key bar. It fills the
// whole terminal; every panel is drawn by panel(), so no line is ever wider than the screen.
// Click targets are recorded with absolute screen positions.
func (m manageModel) renderDashboard() []string {
	width, height := m.width+frameCols, m.height+frameRows
	tabs := m.tabs()
	tab := tabs[m.tab]
	panels := m.loaded && width >= topPanelsMinWidth && height >= topPanelsMinHeight

	screenLines := []string{m.titleLine(width)}
	switch {
	case panels:
		screenLines = append(screenLines, m.renderTopPanels(width)...)
	case m.loaded:
		screenLines = append(screenLines, " "+m.summary())
	default:
		screenLines = append(screenLines, "")
	}

	detailRows := detailPanelRows(width, height)
	if len(tab.lines) == 0 && m.notice == "" {
		detailRows = 1 // nothing is selected: the table takes the room
	}
	mainRows := max(height-len(screenLines)-detailRows-1, 4)

	mainTop := len(screenLines)
	title, spans := m.tabTitle(tabs, width-5)
	for _, span := range spans {
		m.hits.add(mainTop, 3+span.from, span.width, fmt.Sprintf("tab:%d", span.tab))
	}
	screenLines = append(screenLines, panel(title, width, mainRows, colourBorder, m.tableBody(tab, width-4, mainRows-2, mainTop+1))...)
	screenLines = append(screenLines, m.renderDetail(tab, width, detailRows)...)
	return append(screenLines, m.keyLine(len(screenLines), width, m.positionText(tab), m.footerKeys(tab)...))
}

// titleLine is the product name and server on the left and the time of the last update on the right.
func (m manageModel) titleLine(width int) string {
	left := " " + lipglossAccent("◆") + " " + titleStyle.Render("Sentinel")
	if m.report.Server != "" {
		left += dimStyle.Render("  " + m.report.Server)
	}
	updated := "đang tải…"
	if m.loaded {
		updated = "cập nhật " + m.report.GeneratedAt.Local().Format("15:04:05")
	}
	right := dimStyle.Render(updated) + " "
	if pad := width - lipgloss.Width(left) - lipgloss.Width(right); pad > 0 {
		return left + strings.Repeat(" ", pad) + right
	}
	return left
}

// positionText is "2/5": which line of the tab is selected.
func (m manageModel) positionText(tab manageTab) string {
	if len(tab.lines) == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", m.cursor+1, len(tab.lines))
}

// footerKeys are the key hints of the main screen; they follow the tab and, on the Config tab, the
// kind of entry that is selected, so a key that does nothing there is not offered.
func (m manageModel) footerKeys(tab manageTab) []string {
	var keys []string
	switch m.tab {
	case backupsTab:
		keys = []string{"b", "backup", "x", "dừng", "s", "snapshots", "a", "thêm", "e", "sửa", "d", "xoá"}
	case doctorTab:
		keys = []string{"tab", "chuyển", "↑↓", "chọn", "r", "chạy check"}
	case configTab:
		keys = []string{"a", "thêm", "e", "sửa", "d", "xoá"}
		entry, _ := m.selectedConfig()
		switch {
		case entry.name == "":
			keys = []string{"a", "thêm"}
		case entry.section == secSettings:
			keys = []string{"e", "sửa"}
		case entry.section == secNotifier:
			keys = append(keys, "t", "gửi thử")
		}
		keys = append(keys, "tab", "chuyển")
	default:
		keys = []string{"tab", "chuyển", "↑↓", "chọn", "r", "làm mới"}
	}
	return append(keys, "?", "trợ giúp", "q", "thoát")
}

// tabSpan is where one tab's pill sits inside the title: from is the column within the title.
type tabSpan struct{ tab, from, width int }

// tabTitle is the tab bar that sits in the top border of the table's panel: the open tab is a
// filled pill, a tab with a problem is red. limit is the room it has.
func (m manageModel) tabTitle(tabs []manageTab, limit int) (string, []tabSpan) {
	gap := m.tabGap()
	var pills []string
	var spans []tabSpan
	column := 0
	for i, label := range m.tabLabels(tabs, limit) {
		label = " " + label + " "
		spans = append(spans, tabSpan{tab: i, from: column, width: lipgloss.Width(label)})
		column += lipgloss.Width(label) + lipgloss.Width(gap)
		switch {
		case i == m.tab:
			pills = append(pills, activeTab.Render(label))
		case tabs[i].alarm:
			pills = append(pills, severityStyle(sevBad).Render(label))
		default:
			pills = append(pills, dimStyle.Render(label))
		}
	}
	return strings.Join(pills, gap), spans
}

// tabLabels are the labels of the tabs, e.g. "1 Backups 3"; the title joins them with tabGap().
// With less than limit cells the counts go first, then the names of the tabs that are not open.
func (m manageModel) tabLabels(tabs []manageTab, limit int) []string {
	var labels []string
	for _, attempt := range []struct{ counts, names bool }{{true, true}, {false, true}, {false, false}} {
		labels = m.labelsOf(tabs, attempt.counts, attempt.names)
		if m.pillsWidth(labels) <= limit {
			break
		}
	}
	return labels
}

// labelsOf are the tab labels with or without the counts and the names of the tabs that are not open.
func (m manageModel) labelsOf(tabs []manageTab, counts, names bool) []string {
	var labels []string
	for i, tab := range tabs {
		label := fmt.Sprintf("%d", i+1)
		if names || i == m.tab {
			label += " " + tab.title
		}
		if counts && tab.count > 0 {
			label += fmt.Sprintf(" %d", tab.count)
		}
		labels = append(labels, label)
	}
	return labels
}

// pillsWidth is the width of the labels as pills, with the gaps between them.
func (m manageModel) pillsWidth(labels []string) int {
	total := lipgloss.Width(m.tabGap()) * (len(labels) - 1)
	for _, label := range labels {
		total += lipgloss.Width(label) + 2 // the pill's padding
	}
	return total
}

// tabGap separates the tab pills; a narrow screen packs them closer.
func (m manageModel) tabGap() string {
	if m.width < 60 {
		return ""
	}
	return " "
}

// noChecksText says why there is no uptime result: no check is configured, or none has run yet.
func (m manageModel) noChecksText() string {
	if len(m.config.overview.Uptime) > 0 {
		return "Chưa có kết quả: cần chạy `sentinel run`."
	}
	return "Chưa có uptime check nào."
}

// emptyText is what a tab says when it has no line.
func (m manageModel) emptyText(tab int) string {
	switch tab {
	case backupsTab:
		return "Chưa có backup nào. Chạy `sentinel backup run <job>` hoặc bật daemon."
	case uptimeTab:
		return m.noChecksText() + " Cấu hình ở tab 5 Config."
	case alertsTab:
		return "Không có alert nào đang mở."
	case doctorTab:
		return m.doctorWaitText()
	}
	return "Không đọc được config."
}

// tableBody is the inside of the table's panel: the table, or the text of an empty tab.
func (m manageModel) tableBody(tab manageTab, inner, capacity, firstRow int) []string {
	var body []string
	if m.loadErr != nil {
		body = append(body, severityStyle(sevBad).Render("không đọc được store: "+m.loadErr.Error()))
		capacity, firstRow = capacity-1, firstRow+1
	}
	if len(tab.lines) > 0 {
		return append(body, m.renderTable(tab, inner, capacity, firstRow)...)
	}
	empty := wrapLines(m.emptyText(m.tab), max(inner-8, 10))
	for range max((capacity-len(empty))/3, 0) {
		body = append(body, "")
	}
	for _, text := range empty {
		body = append(body, lipgloss.PlaceHorizontal(inner, lipgloss.Center, dimStyle.Render(text)))
	}
	return body
}

// detailPanelRows is the height of the panel under the table: the taller the screen, the more text
// it holds. A small screen gets one line instead of a panel.
func detailPanelRows(width, height int) int {
	switch {
	case width < 60 || height < 18:
		return 1
	case height >= 30:
		return 7
	case height >= 24:
		return 6
	}
	return 5
}

// renderDetail is the panel under the table: the selected line as label/value pairs and its
// sentence, and the outcome of the last action at the bottom.
func (m manageModel) renderDetail(tab manageTab, width, rows int) []string {
	inner := width - 4
	var line manageLine
	hasLine := m.cursor < len(tab.lines)
	if hasLine {
		line = tab.lines[m.cursor]
	}
	if rows == 1 {
		return []string{m.detailLine(line, hasLine, width)}
	}
	notice := m.noticeText(inner)
	room := rows - 2 - len(notice)
	body := detailText(line, hasLine, inner, room)
	for len(body) < room {
		body = append(body, "")
	}
	return panel(m.detailTitle(line, hasLine), width, rows, colourBorder, append(body, notice...))
}

// detailLine is the one line a small screen shows instead of the panel: the outcome of the last
// action, or else the selected line's sentence.
func (m manageModel) detailLine(line manageLine, hasLine bool, width int) string {
	switch {
	case m.notice != "":
		return " " + m.noticeStyle().Render(truncate(m.notice, width-2))
	case hasLine:
		return " " + dimStyle.Render(truncate(line.detail, width-2))
	}
	return ""
}

// detailText is what the panel says about a line in room rows: its label/value pairs, then, for a
// backup, the sentence about its result (the error, or the progress), which gets a full width.
// A line without pairs shows its sentence.
func detailText(line manageLine, hasLine bool, inner, room int) []string {
	switch {
	case !hasLine:
		return nil
	case len(line.fields) == 0 || line.placeholder:
		return clipText(wrapLines(line.detail, inner), room, inner)
	}
	body := kvColumns(line.fields, inner, min(detailFieldRows, room))
	body = body[:min(len(body), room)]
	if line.job == "" { // a config entry: the pairs already say everything
		return body
	}
	outcome := strings.TrimPrefix(line.detail, line.job+" → "+line.repository+": ")
	if outcome == "ok" {
		return body
	}
	return append(body, clipText(wrapLines(outcome, inner), room-len(body), inner)...)
}

// noticeText is the outcome of the last action, at most two lines, coloured by how it went.
func (m manageModel) noticeText(inner int) []string {
	if m.notice == "" {
		return nil
	}
	var lines []string
	for _, text := range clipText(wrapLines(m.notice, inner), 2, inner) {
		lines = append(lines, m.noticeStyle().Render(text))
	}
	return lines
}

func (m manageModel) noticeStyle() lipgloss.Style {
	if m.noticeBad {
		return severityStyle(sevBad)
	}
	return severityStyle(sevOK)
}

// clipText keeps the first limit lines of wrapped text, ending the last with an ellipsis when
// lines were cut. The lines must be plain text.
func clipText(lines []string, limit, width int) []string {
	if len(lines) <= limit {
		return lines
	}
	lines = lines[:max(limit, 0)]
	if len(lines) > 0 {
		lines[len(lines)-1] = truncate(lines[len(lines)-1], max(width-2, 1)) + " …"
	}
	return lines
}

// detailTitle names the selected line in the border of the detail panel.
func (m manageModel) detailTitle(line manageLine, hasLine bool) string {
	switch {
	case !hasLine || line.placeholder:
		return dimStyle.Render("chi tiết")
	case line.job != "":
		return titleStyle.Render(line.job) + dimStyle.Render(" → "+line.repository)
	case m.tab == configTab:
		return dimStyle.Render(sectionNames[line.entry.section]+" · ") + titleStyle.Render(line.entry.name)
	}
	return titleStyle.Render(line.cells[0])
}

// panelTitle is the bold coloured name in the border of a panel.
func panelTitle(text string, colour color.Color) string {
	return lipgloss.NewStyle().Bold(true).Foreground(colour).Render(text)
}

// renderTopPanels is the row of three panels above the tabs: the overview, the backups that
// are running now and the uptime checks. A panel whose subject has a problem gets a red border.
func (m manageModel) renderTopPanels(width int) []string {
	overviewWidth := min(max(width/5, 26), 34)
	runningWidth := (width - overviewWidth) * 11 / 20
	uptimeWidth := width - overviewWidth - runningWidth
	counts := m.healthCounts()

	overviewBorder, uptimeBorder, runningBorder := colourAcc, colourOK, colourMuted
	if counts.failed+counts.alerts+counts.checksDown > 0 {
		overviewBorder = colourBad
	}
	if counts.checksDown > 0 {
		uptimeBorder = colourBad
	}
	if len(m.report.Running) > 0 {
		runningBorder = colourWarn
	}
	return joinColumns([][]string{
		panel(panelTitle("tổng quan", overviewBorder), overviewWidth, topPanelRows, overviewBorder, m.overviewBody()),
		panel(panelTitle("đang chạy", runningBorder), runningWidth, topPanelRows, runningBorder, m.runningBody(runningWidth-4)),
		panel(panelTitle("uptime", uptimeBorder), uptimeWidth, topPanelRows, uptimeBorder, m.uptimeBody(uptimeWidth-4)),
	}, 0)
}

// overviewBody counts what the server has: jobs by state, open alerts, checks and repositories.
func (m manageModel) overviewBody() []string {
	counts := m.healthCounts()
	label := func(text string) string { return dimStyle.Render(fmt.Sprintf("%-8s", text)) }
	number := func(count int, level severity) string {
		if count == 0 {
			return dimStyle.Render("0")
		}
		return severityStyle(level).Render(fmt.Sprintf("%d", count))
	}

	var jobStates []string
	if counts.healthy > 0 {
		jobStates = append(jobStates, severityStyle(sevOK).Render(fmt.Sprintf("✓%d", counts.healthy)))
	}
	if counts.warning+counts.neverRun > 0 {
		jobStates = append(jobStates, severityStyle(sevWarn).Render(fmt.Sprintf("▲%d", counts.warning+counts.neverRun)))
	}
	if counts.failed > 0 {
		jobStates = append(jobStates, severityStyle(sevBad).Render(fmt.Sprintf("✗%d", counts.failed)))
	}
	jobs := counts.healthy + counts.warning + counts.neverRun + counts.failed

	uptimeState := dimStyle.Render("—")
	switch {
	case counts.checksDown > 0:
		uptimeState = severityStyle(sevBad).Render(fmt.Sprintf("✗%d down", counts.checksDown))
	case len(m.report.Checks) > 0:
		uptimeState = severityStyle(sevOK).Render("đều UP")
	}

	seenRepositories := map[string]bool{}
	var storedBytes int64
	for _, jobRepo := range m.jobRepositories {
		seenRepositories[jobRepo.repository] = true
	}
	for _, backup := range m.report.Jobs {
		seenRepositories[backup.Repository] = true
		storedBytes += backup.TotalBytes
	}
	return []string{
		label("Jobs") + titleStyle.Render(fmt.Sprintf("%d", jobs)) + "  " + strings.Join(jobStates, " "),
		label("Alerts") + number(counts.alerts, sevBad),
		label("Uptime") + titleStyle.Render(fmt.Sprintf("%d", len(m.report.Checks))) + "  " + uptimeState,
		label("Repos") + titleStyle.Render(fmt.Sprintf("%d", len(seenRepositories))) + "  " + dimStyle.Render(humanBytes(storedBytes)),
	}
}

// runningBody shows the backup in progress with a progress bar, or what runs next when none is.
func (m manageModel) runningBody(inner int) []string {
	now := m.report.GeneratedAt
	running := m.report.Running
	switch len(running) {
	case 0:
		lines := []string{dimStyle.Render("Không có backup nào đang chạy")}
		if job, when, ok := m.nextScheduled(); ok {
			lines = append(lines, "", dimStyle.Render("Lần tới  ")+titleStyle.Render(job), dimStyle.Render(truncate(runDay(when)+" ("+inFuture(when.Sub(now))+")", inner)))
		}
		return lines
	case 1:
		current := running[0]
		level := sevRun
		switch {
		case current.Stalled:
			level = sevBad
		case current.WillExceedTimeout:
			level = sevWarn
		}
		lines := []string{titleStyle.Render(current.Job) + dimStyle.Render(" → "+current.Repository)}
		switch {
		case current.Phase == backup.PhaseRetention:
			lines = append(lines, dimStyle.Render("đang áp dụng retention"))
		case current.Percent != nil:
			lines = append(lines, progressBar(*current.Percent, max(inner-6, 8), level)+fmt.Sprintf(" %3.0f%%", *current.Percent))
		default:
			lines = append(lines, dimStyle.Render("đã xử lý ")+humanBytes(current.BytesDone))
		}
		lines = append(lines, dimStyle.Render(truncate(runningProgress(current), inner)))
		if warning := runningWarning(current); warning != "" {
			lines = append(lines, severityStyle(level).Render(truncate(warning, inner)))
		}
		return lines
	}
	shown := running
	if len(running) > maxPanelItems {
		shown = running[:maxPanelItems-1]
	}
	nameWidth := 0
	for _, current := range shown {
		nameWidth = max(nameWidth, lipgloss.Width(current.Job))
	}
	nameWidth = min(nameWidth, inner/3)
	var lines []string
	for _, current := range shown {
		name := truncate(current.Job, nameWidth)
		line := titleStyle.Render(name) + strings.Repeat(" ", nameWidth-lipgloss.Width(name)+1)
		if current.Percent != nil {
			line += progressBar(*current.Percent, max(inner-nameWidth-7, 4), sevRun) + fmt.Sprintf(" %3.0f%%", *current.Percent)
		} else {
			line += dimStyle.Render(humanBytes(current.BytesDone))
		}
		lines = append(lines, line)
	}
	if len(running) > len(shown) {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("+%d backup khác", len(running)-len(shown))))
	}
	return lines
}

// nextScheduled is the job that runs next according to the config, when the config is known.
func (m manageModel) nextScheduled() (job string, when time.Time, found bool) {
	for _, candidate := range m.config.listing.Jobs {
		spec := m.scheduleOf(candidate)
		if spec == "" || schedule.IsManual(spec) {
			continue
		}
		if runs := schedule.NextRuns(spec, m.report.GeneratedAt, 1); len(runs) > 0 && (!found || runs[0].Before(when)) {
			job, when, found = candidate.Name, runs[0], true
		}
	}
	return job, when, found
}

// uptimeBody is one line per check: its state, its latency and a sparkline of the latest runs.
func (m manageModel) uptimeBody(inner int) []string {
	checks := m.report.Checks
	if len(checks) == 0 {
		return []string{dimStyle.Render(m.noChecksText()), dimStyle.Render("Cấu hình ở tab 5 Config.")}
	}
	shown := checks
	if len(checks) > maxPanelItems {
		shown = checks[:maxPanelItems-1]
	}
	nameWidth := 0
	for _, check := range shown {
		nameWidth = max(nameWidth, lipgloss.Width(check.Name))
	}
	nameWidth = min(nameWidth, 14, inner/2)
	var lines []string
	for _, check := range shown {
		dot, state, latency := severityStyle(sevOK).Render("●"), severityStyle(sevOK).Render("UP  "), fmt.Sprintf("%dms", check.LatencyMs)
		if !check.OK {
			dot, state, latency = severityStyle(sevBad).Render("✗"), severityStyle(sevBad).Render("DOWN"), "—"
		}
		name := truncate(check.Name, nameWidth)
		line := dot + " " + name + strings.Repeat(" ", nameWidth-lipgloss.Width(name)) + " " + state + " " + fmt.Sprintf("%-6s", latency)
		failed := make([]bool, len(check.RecentOK))
		for i, up := range check.RecentOK {
			failed[i] = !up
		}
		if sparkWidth := min(inner-lipgloss.Width(line)-1, recentChecksShown); sparkWidth > 0 && len(check.RecentLatencyMs) > 0 {
			line += " " + sparkline(check.RecentLatencyMs, failed, sparkWidth)
		}
		lines = append(lines, line)
	}
	if len(checks) > len(shown) {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("+%d check khác", len(checks)-len(shown))))
	}
	return lines
}

// recentChecksShown is the widest sparkline of the uptime panel.
const recentChecksShown = 8
