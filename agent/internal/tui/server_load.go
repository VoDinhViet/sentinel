package tui

import (
	"fmt"

	"sentinel/agent/internal/app"
	"sentinel/agent/internal/backup"
	"sentinel/agent/internal/host"
)

// addNetworkLines is one line per network interface: what went through it since the machine booted.
// The counters are totals, so no line is yellow for them: an error from last month would never clear.
func addNetworkLines(report app.HostReport, add func(manageLine)) {
	for _, found := range report.Interfaces {
		received, sent := backup.HumanBytes(int64(found.RxBytes)), backup.HumanBytes(int64(found.TxBytes))
		line := manageLine{
			cells:  []string{found.Name, "↓ " + received + "  ↑ " + sent, fmt.Sprintf("lỗi %d · rớt gói %d", found.RxErrors+found.TxErrors, found.RxDropped+found.TxDropped)},
			detail: fmt.Sprintf("%s: nhận %s, gửi %s từ lúc máy khởi động.", found.Name, received, sent),
		}
		line.fields = [][2]string{{"Nhận", received}, {"Gửi", sent},
			{"Lỗi nhận / gửi", fmt.Sprintf("%d / %d", found.RxErrors, found.TxErrors)},
			{"Rớt gói nhận / gửi", fmt.Sprintf("%d / %d", found.RxDropped, found.TxDropped)}}
		add(line)
	}
	if len(report.Interfaces) == 0 {
		add(manageLine{cells: []string{"Không có card mạng nào", "", ""}, detail: "Không đọc được card mạng (bỏ qua lo và veth*)."})
	}
}

// addProcessLines is the processes that use the most CPU, then the most memory. Only names: the
// arguments of a process can hold a password, so Sentinel never reads them.
func addProcessLines(report app.HostReport, add func(manageLine)) {
	for _, proc := range report.TopCPU {
		add(processLine("CPU", proc, fmt.Sprintf("%.0f%%", proc.CPUPercent), "RAM "+backup.HumanBytes(int64(proc.MemoryBytes))))
	}
	for _, proc := range report.TopMemory {
		add(processLine("RAM", proc, backup.HumanBytes(int64(proc.MemoryBytes)), fmt.Sprintf("CPU %.0f%%", proc.CPUPercent)))
	}
	if len(report.TopCPU) == 0 && len(report.TopMemory) == 0 {
		add(manageLine{cells: []string{"Không đọc được tiến trình", "", ""}, detail: "Không liệt kê được tiến trình của máy này."})
	}
}

func processLine(kind string, proc host.Process, value, other string) manageLine {
	name := fmt.Sprintf("%s %s (%d)", kind, proc.Name, proc.PID)
	return manageLine{
		cells:  []string{name, value, other},
		detail: fmt.Sprintf("%s: %s, %s.", proc.Name, value, other),
		fields: [][2]string{{"Tên", proc.Name}, {"PID", fmt.Sprint(proc.PID)}, {"CPU", fmt.Sprintf("%.1f%%", proc.CPUPercent)}, {"RAM", backup.HumanBytes(int64(proc.MemoryBytes))}},
	}
}
