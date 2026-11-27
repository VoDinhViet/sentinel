package tui

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/host"
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

// buildServerTab is the Server tab: how this machine is doing.
func (m manageModel) buildServerTab() manageTab { return serverTabOf(m.server.report) }

// serverTabOf is the lines of a reading of a machine: this one, or another seen through its API.
func serverTabOf(report app.HostReport) manageTab {
	tab := manageTab{title: "Server", statusCol: -1, historyCol: -1, gaugeCol: 1}
	if report.CPUs == 0 { // nothing read yet: a reading always has at least one CPU
		return tab
	}
	add := func(line manageLine) { tab.lines = append(tab.lines, line) }
	section := func(title, hint string, headers []string) {
		tab.sections = append(tab.sections, tableSection{title: title, hint: hint, headers: headers, first: len(tab.lines)})
	}

	section("MÁY", machineText(report), []string{"MỤC", "ĐANG DÙNG", "CHI TIẾT"})
	addResourceLines(report, add)
	addDiskLines(report, add)
	addReadErrorLines(report, add)
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

// machineText is the hint of the first section: which machine this is and how long it has run. The
// kernel is longer than a narrow screen has room for, so it sits in the detail panel of the CPU line.
func machineText(report app.HostReport) string {
	parts := []string{report.Hostname, report.OS, "chạy " + uptimeText(report.Uptime)}
	return strings.Join(slices.DeleteFunc(parts, func(part string) bool { return part == "" }), " · ")
}

// gaugeLine is a line whose second column is a usage bar; the third says in words what the bar counts.
func gaugeLine(level severity, name string, usedPercent float64, detail string) manageLine {
	return manageLine{cells: []string{name, "", detail}, severity: level, gauge: true, usedPercent: usedPercent}
}

// addResourceLines is CPU, RAM and swap, each with a bar of how much is in use.
func addResourceLines(report app.HostReport, add func(manageLine)) {
	loadLevel := loadSeverity(report.Load[0], report.CPUs)
	cpuLevel := levelByThreshold(report.CPUPercent, cpuWarnPercent, math.Inf(1))
	cpu := gaugeLine(max(loadLevel, cpuLevel), "CPU", report.CPUPercent,
		fmt.Sprintf("load %.2f %.2f %.2f · %d CPU", report.Load[0], report.Load[1], report.Load[2], report.CPUs))
	cpu.sub = cpuReason(report, loadLevel, cpuLevel)
	cpu.detail = fmt.Sprintf("CPU bận %.0f%% kể từ lần đọc trước; %s.", report.CPUPercent, cpu.cells[2])
	cpu.fields = [][2]string{{"Bận", fmt.Sprintf("%.0f%%", report.CPUPercent)}, {"Số CPU", fmt.Sprint(report.CPUs)},
		{"Load 1 phút", fmt.Sprintf("%.2f", report.Load[0])}, {"Load 5 phút", fmt.Sprintf("%.2f", report.Load[1])}, {"Load 15 phút", fmt.Sprintf("%.2f", report.Load[2])}, {"Kernel", report.Kernel}}
	add(cpu)

	memory := report.Memory
	used := memory.TotalBytes - min(memory.AvailableBytes, memory.TotalBytes)
	availablePercent := percentOf(memory.AvailableBytes, memory.TotalBytes)
	ram := gaugeLine(memorySeverity(availablePercent), "RAM", percentOf(used, memory.TotalBytes),
		fmt.Sprintf("%s / %s · còn %s", backup.HumanBytes(int64(used)), backup.HumanBytes(int64(memory.TotalBytes)), backup.HumanBytes(int64(memory.AvailableBytes))))
	ram.detail = "RAM: " + ram.cells[2] + "."
	ram.fields = [][2]string{{"Đã dùng", backup.HumanBytes(int64(used))}, {"Tổng", backup.HumanBytes(int64(memory.TotalBytes))}, {"Khả dụng", backup.HumanBytes(int64(memory.AvailableBytes))}}
	if ram.severity != sevOK {
		ram.sub = fmt.Sprintf("RAM khả dụng còn %.0f%%: vàng dưới %d%%, đỏ dưới %d%%", availablePercent, memoryWarnAvailablePercent, memoryBadAvailablePercent)
	}
	add(ram)

	add(swapLine(memory))
}

// swapLine is the swap, or a plain line when the machine has none.
func swapLine(memory host.Memory) manageLine {
	if memory.SwapTotalBytes == 0 {
		return manageLine{cells: []string{"Swap", "không có swap", ""}, detail: "Máy này không có swap."}
	}
	used := memory.SwapTotalBytes - min(memory.SwapFreeBytes, memory.SwapTotalBytes)
	usedPercent := percentOf(used, memory.SwapTotalBytes)
	swap := gaugeLine(levelByThreshold(usedPercent, swapWarnUsedPercent, math.Inf(1)), "Swap", usedPercent,
		fmt.Sprintf("%s / %s", backup.HumanBytes(int64(used)), backup.HumanBytes(int64(memory.SwapTotalBytes))))
	swap.detail = "Swap: " + swap.cells[2] + "."
	swap.fields = [][2]string{{"Đã dùng", backup.HumanBytes(int64(used))}, {"Tổng", backup.HumanBytes(int64(memory.SwapTotalBytes))}}
	if swap.severity != sevOK {
		swap.sub = fmt.Sprintf("swap đã dùng %.0f%%: vàng khi quá %d%%", usedPercent, swapWarnUsedPercent)
	}
	return swap
}

// cpuReason says why the CPU line is yellow or red; it is empty when it is not.
func cpuReason(report app.HostReport, loadLevel, cpuLevel severity) string {
	switch {
	case loadLevel != sevOK:
		return fmt.Sprintf("load %.2f trên mỗi CPU: vàng từ %.0f, đỏ từ %.0f", report.Load[0]/float64(report.CPUs), loadWarnPerCPU, loadBadPerCPU)
	case cpuLevel != sevOK:
		return fmt.Sprintf("CPU bận %.0f%%: vàng từ %d%%", report.CPUPercent, cpuWarnPercent)
	}
	return ""
}

// addDiskLines is one line per disk, in the same table as the CPU and RAM so the bars line up.
func addDiskLines(report app.HostReport, add func(manageLine)) {
	for _, found := range report.Disks {
		freeText, totalText := backup.HumanBytes(int64(found.FreeBytes)), backup.HumanBytes(int64(found.TotalBytes))
		disk := gaugeLine(diskSeverity(found.FreePercent(), report.MinFreePercent), "Đĩa "+found.Mount, 100-found.FreePercent(),
			fmt.Sprintf("còn %s / %s · %s", freeText, totalText, found.FSType))
		disk.detail = fmt.Sprintf("%s: còn trống %s trên %s (%.0f%%).", found.Mount, freeText, totalText, found.FreePercent())
		disk.sub = diskReason(found, disk.severity, report.MinFreePercent)
		disk.fields = [][2]string{{"Mount", found.Mount}, {"Thiết bị", found.Device}, {"FS", found.FSType}, {"Trống", freeText}, {"Tổng", totalText},
			{"Alert khi trống dưới", fmt.Sprintf("%d%%", report.MinFreePercent)}}
		add(disk)
	}
	if len(report.Disks) == 0 {
		add(manageLine{cells: []string{"Đĩa", "không đọc được ổ đĩa nào", ""}, severity: sevWarn, detail: "Không liệt kê được ổ đĩa của máy này."})
	}
}

// diskReason says why a disk is yellow or red; it is empty when it is not.
func diskReason(found host.Disk, level severity, minFreePercent int) string {
	switch level {
	case sevBad:
		return fmt.Sprintf("còn trống %.0f%%, dưới ngưỡng alert %d%%", found.FreePercent(), minFreePercent)
	case sevWarn:
		return fmt.Sprintf("sắp đầy: còn trống %.0f%%, Sentinel báo alert khi dưới %d%%", found.FreePercent(), minFreePercent)
	}
	return ""
}

// addReadErrorLines is whatever could not be read, one line each.
func addReadErrorLines(report app.HostReport, add func(manageLine)) {
	for _, err := range report.Errs {
		add(manageLine{cells: []string{"Không đọc được", firstLine(err.Error()), ""}, severity: sevWarn, detail: err.Error()})
	}
}

// unitStateText is the state of a systemd unit in words.
func unitStateText(state string) string {
	switch state {
	case "active":
		return "đang chạy"
	case "inactive":
		return "đã dừng"
	case "failed":
		return "lỗi"
	}
	return state
}

// addServiceLines is the Dịch vụ section: Sentinel's own unit, then every unit that failed.
func addServiceLines(report app.HostReport, add func(manageLine)) {
	sentinelLevel := sevOK
	if report.Sentinel.Active != "active" {
		sentinelLevel = sevWarn
	}
	add(manageLine{
		cells:    []string{report.Sentinel.Name, unitStateText(report.Sentinel.Active), "daemon backup, uptime check và cảnh báo của Sentinel"},
		severity: sentinelLevel,
		detail:   report.Sentinel.Name + ": " + report.Sentinel.Active + ". Daemon không chạy thì không có backup theo lịch.",
	})
	for _, failedUnit := range report.FailedUnits {
		add(manageLine{cells: []string{failedUnit.Name, unitStateText(failedUnit.Active), failedUnit.Description}, severity: sevBad, detail: failedUnit.Name + " bị lỗi: " + failedUnit.Description})
	}
	if len(report.FailedUnits) == 0 {
		add(manageLine{cells: []string{"Không có unit nào lỗi", "", ""}, detail: "systemd không báo unit nào ở trạng thái failed."})
	}
}

// serverWaitText is what the tab says while it has nothing to show.
func (m manageModel) serverWaitText() string {
	if m.actions.host == nil {
		return "Không đọc được tình trạng máy."
	}
	return "Đang đọc tình trạng máy…"
}
