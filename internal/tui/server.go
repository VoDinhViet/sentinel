package tui

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/internal/app"
	"sentinel/internal/backup"
)

// serverTab is the index of the Server tab.
const serverTab = 5

// hostReadTimeout bounds one reading of the machine: a hung systemctl or network share must not
// leave the tab waiting for ever.
const hostReadTimeout = 10 * time.Second

// What turns a value of the Server tab yellow or red. Disks use the share the disk alert uses.
const (
	memoryWarnAvailablePercent = 10
	memoryBadAvailablePercent  = 5
	swapWarnUsedPercent        = 50
	cpuWarnPercent             = 90
	loadWarnPerCPU             = 1.0
	loadBadPerCPU              = 2.0
)

// addServerLine adds a line to the Server tab: its cells, the sentence under the table and the
// label/value pairs of the detail panel.
type addServerLine func(level severity, cells []string, detail string, fields [][2]string)

// hostReadMsg carries the reading started by startServer.
type hostReadMsg struct{ report app.HostReport }

// serverState is what the Server tab shows: nothing yet, a reading in progress, or the last reading.
type serverState struct {
	started bool // read at least once; the first reading starts when the tab is first opened
	reading bool
	report  app.HostReport
}

// startServerIfNeeded starts the first reading when the Server tab is on screen. Update calls it
// after every message, so every way into the tab starts it.
func (m manageModel) startServerIfNeeded(cmd tea.Cmd) (manageModel, tea.Cmd) {
	if !m.onTabs() || m.tab != serverTab || m.server.started {
		return m, cmd
	}
	next, start := m.startServer()
	return next, tea.Batch(cmd, start)
}

// startServer reads the machine in the background, unless a reading is already under way. The timed
// refresh and the r key both come here, so readings never pile up behind a slow systemctl.
func (m manageModel) startServer() (manageModel, tea.Cmd) {
	if m.actions.host == nil || m.server.reading {
		return m, nil
	}
	m.server.started, m.server.reading = true, true
	read, ctx := m.actions.host, m.ctx
	return m, func() tea.Msg {
		readCtx, cancel := context.WithTimeout(ctx, hostReadTimeout)
		defer cancel()
		return hostReadMsg{report: read(readCtx)}
	}
}

// refreshServer is what the timed refresh does for this tab: read again, but only while it is on screen.
func (m manageModel) refreshServer() (manageModel, tea.Cmd) {
	if !m.onTabs() || m.tab != serverTab {
		return m, nil
	}
	return m.startServer()
}

// levelByThreshold is the level of a value: bad from bad up, a warning from warn up.
func levelByThreshold(value, warn, bad float64) severity {
	switch {
	case value >= bad:
		return sevBad
	case value >= warn:
		return sevWarn
	}
	return sevOK
}

// diskSeverity: below the alert share is bad, below twice that is a warning.
func diskSeverity(freePercent float64, minFreePercent int) severity {
	switch {
	case freePercent < float64(minFreePercent):
		return sevBad
	case freePercent < float64(2*minFreePercent):
		return sevWarn
	}
	return sevOK
}

// memorySeverity is judged by what is still available, not by what is free: the page cache is not a problem.
func memorySeverity(availablePercent float64) severity {
	switch {
	case availablePercent < memoryBadAvailablePercent:
		return sevBad
	case availablePercent < memoryWarnAvailablePercent:
		return sevWarn
	}
	return sevOK
}

// loadSeverity compares the 1 minute load with the number of CPUs.
func loadSeverity(load float64, cpus int) severity {
	perCPU := load / float64(max(cpus, 1))
	return levelByThreshold(perCPU, loadWarnPerCPU, loadBadPerCPU)
}

func severityWord(level severity) string {
	switch level {
	case sevBad:
		return "LỖI"
	case sevWarn:
		return "cảnh báo"
	}
	return "ok"
}

// uptimeText is how long the machine has been up, e.g. "12 ngày 3 giờ".
func uptimeText(uptime time.Duration) string {
	days, hours, minutes := int(uptime.Hours())/24, int(uptime.Hours())%24, int(uptime.Minutes())%60
	switch {
	case days > 0:
		return fmt.Sprintf("%d ngày %d giờ", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d giờ %d phút", hours, minutes)
	}
	return fmt.Sprintf("%d phút", minutes)
}

func percentOf(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

func (m manageModel) buildServerTab() manageTab {
	tab := manageTab{title: "Server", statusCol: 1, historyCol: -1}
	report := m.server.report
	if report.CPUs == 0 { // nothing read yet: a reading always has at least one CPU
		return tab
	}
	add := func(level severity, cells []string, detail string, fields [][2]string) {
		tab.lines = append(tab.lines, manageLine{cells: cells, severity: level, detail: detail, fields: fields})
	}
	section := func(title, hint string, headers []string) {
		tab.sections = append(tab.sections, tableSection{title: title, hint: hint, headers: headers, first: len(tab.lines)})
	}

	section("HỆ THỐNG", "máy này lúc này", []string{"MỤC", "TRẠNG THÁI", "GIÁ TRỊ"})
	addSystemLines(report, add)

	section("Ổ ĐĨA", "báo lỗi khi trống dưới "+fmt.Sprint(report.MinFreePercent)+"%", []string{"MOUNT", "TRẠNG THÁI", "TRỐNG", "TỔNG", "FS"})
	for _, found := range report.Disks {
		level := diskSeverity(found.FreePercent(), report.MinFreePercent)
		free := fmt.Sprintf("%s (%.0f%%)", backup.HumanBytes(int64(found.FreeBytes)), found.FreePercent())
		total := backup.HumanBytes(int64(found.TotalBytes))
		add(level, []string{found.Mount, severityWord(level), free, total, found.FSType},
			fmt.Sprintf("%s: còn trống %s trên %s", found.Mount, free, total),
			[][2]string{{"Mount", found.Mount}, {"Thiết bị", found.Device}, {"FS", found.FSType}, {"Trống", free}, {"Tổng", total}})
	}
	if len(report.Disks) == 0 {
		add(sevWarn, []string{"không đọc được ổ đĩa nào", "cảnh báo", ""}, "Không liệt kê được ổ đĩa của máy này.", nil)
	}

	if report.Systemd {
		section("DỊCH VỤ", "systemd", []string{"UNIT", "TRẠNG THÁI", "MÔ TẢ"})
		addServiceLines(report, add)
	}

	for _, line := range tab.lines {
		if line.severity != sevOK {
			tab.count++
		}
		tab.alarm = tab.alarm || line.severity == sevBad
	}
	return tab
}

// addSystemLines is the Hệ thống section: the machine, how long it has run, the load, CPU, RAM and
// swap, then whatever could not be read.
func addSystemLines(report app.HostReport, add addServerLine) {
	machine := strings.TrimSpace(report.Hostname + " · " + report.OS + " · kernel " + report.Kernel)
	add(sevOK, []string{"Máy", "ok", machine}, machine, [][2]string{{"Hostname", report.Hostname}, {"Hệ điều hành", report.OS}, {"Kernel", report.Kernel}})
	add(sevOK, []string{"Uptime", "ok", uptimeText(report.Uptime)}, "Máy đã chạy liên tục "+uptimeText(report.Uptime)+".", nil)

	loadLevel := loadSeverity(report.Load[0], report.CPUs)
	loadValue := fmt.Sprintf("%.2f %.2f %.2f · %d CPU", report.Load[0], report.Load[1], report.Load[2], report.CPUs)
	add(loadLevel, []string{"Load", severityWord(loadLevel), loadValue}, "Load trung bình 1, 5, 15 phút: "+loadValue+".", nil)

	cpuLevel := levelByThreshold(report.CPUPercent, cpuWarnPercent, math.Inf(1))
	cpuValue := fmt.Sprintf("%.0f%%", report.CPUPercent)
	add(cpuLevel, []string{"CPU", severityWord(cpuLevel), cpuValue}, "CPU bận "+cpuValue+" kể từ lần đọc trước.", nil)

	memory := report.Memory
	availablePercent := percentOf(memory.AvailableBytes, memory.TotalBytes)
	memoryLevel := memorySeverity(availablePercent)
	memoryValue := fmt.Sprintf("dùng %s / %s · còn %.0f%%", backup.HumanBytes(int64(memory.TotalBytes-min(memory.AvailableBytes, memory.TotalBytes))), backup.HumanBytes(int64(memory.TotalBytes)), availablePercent)
	add(memoryLevel, []string{"RAM", severityWord(memoryLevel), memoryValue}, "RAM: "+memoryValue+".", nil)

	swapValue, swapLevel := "không có swap", sevOK
	if memory.SwapTotalBytes > 0 {
		swapUsed := memory.SwapTotalBytes - min(memory.SwapFreeBytes, memory.SwapTotalBytes)
		swapLevel = levelByThreshold(percentOf(swapUsed, memory.SwapTotalBytes), swapWarnUsedPercent, math.Inf(1))
		swapValue = fmt.Sprintf("dùng %s / %s", backup.HumanBytes(int64(swapUsed)), backup.HumanBytes(int64(memory.SwapTotalBytes)))
	}
	add(swapLevel, []string{"Swap", severityWord(swapLevel), swapValue}, "Swap: "+swapValue+".", nil)

	for _, err := range report.Errs {
		add(sevWarn, []string{"Không đọc được", "cảnh báo", firstLine(err.Error())}, err.Error(), nil)
	}
}

// addServiceLines is the Dịch vụ section: Sentinel's own unit, then every unit that failed.
func addServiceLines(report app.HostReport, add addServerLine) {
	sentinelLevel := sevOK
	if report.Sentinel.Active != "active" {
		sentinelLevel = sevWarn
	}
	add(sentinelLevel, []string{report.Sentinel.Name, report.Sentinel.Active, "daemon backup, uptime check và cảnh báo của Sentinel"},
		report.Sentinel.Name+": "+report.Sentinel.Active+". Daemon không chạy thì không có backup theo lịch.", nil)
	for _, failedUnit := range report.FailedUnits {
		add(sevBad, []string{failedUnit.Name, failedUnit.Active, failedUnit.Description}, failedUnit.Name+" bị lỗi: "+failedUnit.Description, nil)
	}
	if len(report.FailedUnits) == 0 {
		add(sevOK, []string{"không có unit nào lỗi", "ok", ""}, "systemd không báo unit nào ở trạng thái failed.", nil)
	}
}

// serverWaitText is what the tab says while it has nothing to show.
func (m manageModel) serverWaitText() string {
	if m.actions.host == nil {
		return "Không đọc được tình trạng máy."
	}
	return "Đang đọc tình trạng máy…"
}
