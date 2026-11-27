package tui

import (
	"fmt"
	"strings"
	"time"

	"sentinel/pkg/agentapi"
	"sentinel/server/internal/ctlapi"
	"sentinel/server/internal/format"
)

// The parts of the screen of one server, in the order of the keys 1 to 5.
const (
	hostPart = iota
	backupsPart
	checksPart
	alertsPart
	tasksPart
	partCount
)

// certWarnDays is how close the end of a certificate must be to show as a warning.
const certWarnDays = 14

// serverScreen is one server: what the control plane knows of it, and the latest report it sent.
type serverScreen struct {
	name    string
	detail  ctlapi.AgentDetail // at first only the line of the list; the report comes with the answer
	read    bool               // detail is the answer for this server
	loading bool
	err     error
	current int // the part on show
	cursor  int

	tasks    []ctlapi.Task // the latest tasks given to this server
	tasksErr error
	confirm  *confirmation // a task waiting for the person to say yes
	notice   notice
}

// table is the part on show.
func (s serverScreen) table(now time.Time) table { return s.parts(now)[s.current] }

// parts are the tables of the screen: the machine, the backups, the uptime checks, the alerts the
// server has open, and the tasks it was given.
func (s serverScreen) parts(now time.Time) []table {
	report := s.detail.Report
	if report == nil {
		empty := s.noReportText()
		return []table{{title: "Máy", empty: empty}, {title: "Backup", empty: empty}, {title: "Uptime", empty: empty}, {title: "Alert", empty: empty}, tasksTable(s.tasks, now, s.tasksErr)}
	}
	return []table{hostTable(report.Host), backupsTable(report.Backups, now), checksTable(report.Checks, now), alertsTable(report.Alerts, now), tasksTable(s.tasks, now, s.tasksErr)}
}

func (s serverScreen) noReportText() string {
	switch {
	case s.err != nil:
		return "Không đọc được server này: " + s.err.Error()
	case s.detail.Status == "PENDING":
		return "Server này chưa join: chưa có báo cáo nào."
	case !s.read:
		return "Đang đọc…"
	}
	return "Chưa nhận báo cáo nào từ server này."
}

func hostTable(host agentapi.HostStatus) table {
	t := table{title: "Máy", headers: []string{"MỤC", "GIÁ TRỊ"}, statusCol: 1}
	add := func(of level, label, value string) {
		t.lines = append(t.lines, tableLine{cells: []string{label, value}, level: of, detail: label + ": " + value})
	}
	add(levelNone, "Máy", fmt.Sprintf("%s · %s · kernel %s", format.Dash(host.Hostname), format.Dash(host.OS), format.Dash(host.Kernel)))
	add(levelNone, "Bật được", format.Uptime(host.UptimeSeconds))
	add(levelNone, "Load", fmt.Sprintf("%.2f %.2f %.2f", host.Load[0], host.Load[1], host.Load[2]))
	add(busyLevel(host.CPUPercent), "CPU", fmt.Sprintf("%.0f%% của %d nhân", host.CPUPercent, host.CPUs))
	add(freeLevel(host.Memory.AvailableBytes, host.Memory.TotalBytes, host.MinFreePercent), "RAM", freeText(host.Memory.AvailableBytes, host.Memory.TotalBytes))
	if host.Memory.SwapTotalBytes > 0 {
		add(levelNone, "Swap", freeText(host.Memory.SwapFreeBytes, host.Memory.SwapTotalBytes))
	}
	for _, disk := range host.Disks {
		add(freeLevel(disk.FreeBytes, disk.TotalBytes, host.MinFreePercent), "Ổ đĩa "+disk.Mount, freeText(disk.FreeBytes, disk.TotalBytes))
	}
	if host.Sentinel != nil {
		add(unitLevel(host.Sentinel.Active), host.Sentinel.Name, host.Sentinel.Active)
	}
	for _, unit := range host.FailedServices {
		add(levelBad, "Unit lỗi", unit.Name+" ("+unit.Active+")")
	}
	for _, found := range host.Interfaces {
		add(netLevel(found), "Mạng "+found.Name, fmt.Sprintf("nhận %s · gửi %s · lỗi %d · rơi %d",
			format.Size(found.RxBytes), format.Size(found.TxBytes), found.RxErrors+found.TxErrors, found.RxDropped+found.TxDropped))
	}
	for _, busy := range host.TopProcesses.ByCPU {
		add(levelNone, "Tiến trình CPU", fmt.Sprintf("%s (pid %d) %.0f%%", busy.Name, busy.PID, busy.CPUPercent))
	}
	for _, big := range host.TopProcesses.ByMemory {
		add(levelNone, "Tiến trình RAM", fmt.Sprintf("%s (pid %d) %s", big.Name, big.PID, format.Size(big.MemoryBytes)))
	}
	for _, problem := range host.Errors {
		add(levelWarn, "Không đọc được", problem)
	}
	return t
}

// freeText is "1.2 GiB trống / 8.0 GiB (15% trống)".
func freeText(free, total uint64) string {
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%s trống / %s (%d%% trống)", format.Size(free), format.Size(total), freePercent(free, total))
}

// freePercent is the share of total that is free, 0 to 100, whatever a machine reported: free above total
// is 100, and the sum is done in floats so that a huge number cannot wrap.
func freePercent(free, total uint64) int {
	if total == 0 || free >= total {
		return 100
	}
	return int(float64(free) / float64(total) * 100)
}

// freeLevel is bad when less than minFreePercent of total is free: the same line the agent draws for itself.
func freeLevel(free, total uint64, minFreePercent int) level {
	if total == 0 || minFreePercent <= 0 {
		return levelNone
	}
	if freePercent(free, total) < minFreePercent {
		return levelBad
	}
	return levelNone
}

func busyLevel(cpuPercent float64) level {
	if cpuPercent >= 90 {
		return levelWarn
	}
	return levelNone
}

func unitLevel(active string) level {
	if active == "active" {
		return levelOK
	}
	return levelBad
}

func netLevel(found agentapi.NetInterface) level {
	if found.RxErrors+found.TxErrors > 0 {
		return levelWarn
	}
	return levelNone
}

func backupsTable(backups []agentapi.BackupStatus, now time.Time) table {
	t := table{title: "Backup", headers: []string{"JOB", "REPOSITORY", "KẾT THÚC", "KẾT QUẢ"}, statusCol: 3, empty: "Server này chưa có lần backup nào."}
	for _, backup := range backups {
		of := levelOK
		if !backup.Success {
			of = levelBad
		}
		t.lines = append(t.lines, tableLine{
			cells: []string{backup.Job, backup.Repository, format.Since(backup.FinishedAt, now), backup.Result},
			level: of,
			detail: fmt.Sprintf("%s → %s, %s (+%s, tổng %s): %s", backup.Job, backup.Repository, backup.FinishedAt.Local().Format("2006-01-02 15:04"),
				format.Size(uint64(max(backup.BytesAdded, 0))), format.Size(uint64(max(backup.TotalBytes, 0))), backup.Result),
		})
	}
	return t
}

func checksTable(checks []agentapi.CheckStatus, now time.Time) table {
	t := table{title: "Uptime", headers: []string{"TÊN", "KẾT QUẢ", "ĐỘ TRỄ", "LÚC"}, statusCol: 1, empty: "Server này chưa có kết quả uptime check nào."}
	for _, check := range checks {
		result, of, detail := "UP", levelOK, check.Name+": UP"
		if !check.OK {
			result, of, detail = "DOWN", levelBad, check.Name+": DOWN: "+check.Error
		}
		t.lines = append(t.lines, tableLine{
			cells: []string{check.Name, result, fmt.Sprintf("%dms", check.LatencyMs), format.Since(check.At, now)}, level: of, detail: detail,
		})
	}
	return t
}

func alertsTable(alerts []agentapi.AlertStatus, now time.Time) table {
	t := table{title: "Alert", headers: []string{"KEY", "NỘI DUNG", "TỪ"}, statusCol: 0, empty: "Server này không có alert nào đang mở."}
	for _, open := range alerts {
		t.lines = append(t.lines, alertLine(open.Key, open.Message, open.Since, now, levelWarn))
	}
	return t
}

// alertLine is a line of an alert: its key, what it says and since when.
func alertLine(key, message string, since, now time.Time, of level) tableLine {
	return tableLine{
		cells:  []string{key, message, format.Since(since, now)},
		level:  of,
		detail: fmt.Sprintf("%s: %s (từ %s)", key, message, since.Local().Format("2006-01-02 15:04")),
	}
}

// statusLevel is the colour of a status.
func statusLevel(status string) level {
	switch status {
	case "ONLINE":
		return levelOK
	case "OFFLINE":
		return levelBad
	case "PENDING":
		return levelWarn
	}
	return levelDim
}

// serversTable is the list of servers.
func serversTable(servers []ctlapi.Agent, now time.Time, loaded bool) table {
	t := table{
		title:     "Server",
		headers:   []string{"TÊN", "TRẠNG THÁI", "LẦN CUỐI", "PHIÊN BẢN", "CHỨNG CHỈ HẾT HẠN"},
		statusCol: 1,
		empty:     "Chưa có server nào. Thêm bằng: sentinelctl server add <tên>",
	}
	if !loaded {
		t.empty = "Đang hỏi control plane…"
	}
	for _, server := range servers {
		t.lines = append(t.lines, tableLine{
			cells:  []string{server.Name, format.Status(server.Status, server.State), format.Ago(server.LastSeen, now), format.Dash(server.Version), format.Day(server.CertNotAfter)},
			level:  statusLevel(server.Status),
			detail: serverSentence(server, now),
		})
	}
	return t
}

// serverSentence says in words how a server is. A certificate near its end comes first: it is what
// someone should act on.
func serverSentence(server ctlapi.Agent, now time.Time) string {
	parts := []string{server.Name + ": " + format.Status(server.Status, server.State)}
	cert := ""
	if server.CertNotAfter != nil {
		left := int(server.CertNotAfter.Sub(now).Hours() / 24)
		cert = fmt.Sprintf("chứng chỉ hết hạn %s (còn %d ngày)", format.Day(server.CertNotAfter), left)
		switch {
		case left < 0:
			parts = append(parts, "chứng chỉ ĐÃ HẾT HẠN "+format.Day(server.CertNotAfter))
			cert = ""
		case left < certWarnDays:
			parts = append(parts, fmt.Sprintf("chứng chỉ sắp hết hạn (còn %d ngày)", left))
			cert = ""
		}
	}
	if server.LastSeen == nil {
		parts = append(parts, "chưa thấy lần nào")
	} else {
		parts = append(parts, "thấy "+format.Ago(server.LastSeen, now))
	}
	if server.Version != "" {
		parts = append(parts, "phiên bản "+server.Version)
	}
	if cert != "" {
		parts = append(parts, cert)
	}
	return strings.Join(parts, ", ")
}

// alertsOfControlPlane is the list of the alerts the control plane has open.
func alertsOfControlPlane(alerts []ctlapi.Alert, now time.Time, loaded bool) table {
	t := table{title: "Alert", headers: []string{"KEY", "NỘI DUNG", "TỪ"}, statusCol: 0, empty: "Không có cảnh báo nào đang mở."}
	if !loaded {
		t.empty = "Đang hỏi control plane…"
	}
	for _, open := range alerts {
		of := levelWarn
		if strings.HasPrefix(open.Key, "agent:") { // a server that stopped reporting
			of = levelBad
		}
		t.lines = append(t.lines, alertLine(open.Key, open.Message, open.Since, now, of))
	}
	return t
}
