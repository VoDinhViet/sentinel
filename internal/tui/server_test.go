package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sentinel/internal/app"
	"sentinel/internal/host"
	"sentinel/internal/status"
)

const gib = 1 << 30

// healthyHost is a machine with nothing to complain about.
func healthyHost() app.HostReport {
	return app.HostReport{
		MinFreePercent: 10,
		State: host.State{
			Hostname: "web-1", OS: "ubuntu 24.04", Kernel: "6.8.0", Uptime: 51 * time.Hour, Load: [3]float64{0.4, 0.3, 0.2}, CPUs: 4, CPUPercent: 12,
			Memory:      host.Memory{TotalBytes: 8 * gib, AvailableBytes: 5 * gib, SwapTotalBytes: 2 * gib, SwapFreeBytes: 2 * gib},
			Disks:       []host.Disk{{Mount: "/", Device: "/dev/sda1", FSType: "ext4", FreeBytes: 60 * gib, TotalBytes: 100 * gib}},
			Systemd:     true,
			Sentinel:    host.Unit{Name: "sentinel.service", Active: "active"},
			FailedUnits: nil,
		},
	}
}

// serverModel is a loaded screen whose host action counts its readings and answers with report.
func serverModel(t *testing.T, report app.HostReport) (manageModel, *int) {
	t.Helper()
	readings := new(int)
	actions := manageActions{host: func(context.Context) app.HostReport {
		*readings++
		return report
	}}
	model, _ := newManageModel(context.Background(), func() (status.Report, error) { return sampleReport(), nil }, actions, nil).Update(reportMsg{report: sampleReport()})
	return model.(manageModel), readings
}

// openServerTab presses 6 and delivers the reading the tab started.
func openServerTab(t *testing.T, screen manageModel) manageModel {
	t.Helper()
	screen, cmd := send(t, screen, "6")
	if cmd == nil || !screen.server.reading {
		t.Fatal("opening the tab must start a reading")
	}
	model, _ := screen.Update(cmd())
	return model.(manageModel)
}

func TestServerTabReadsOnFirstVisitAndShowsTheMachine(t *testing.T) {
	screen, readings := serverModel(t, healthyHost())
	if *readings != 0 {
		t.Fatal("the machine must not be read before the tab is opened")
	}
	screen, cmd := send(t, screen, "6")
	if view := plain(screen.render()); !strings.Contains(view, "Đang đọc tình trạng máy") {
		t.Errorf("no progress text while reading:\n%s", view)
	}
	model, _ := screen.Update(cmd())
	screen = model.(manageModel)
	if *readings != 1 || screen.server.reading {
		t.Fatalf("readings = %d, reading = %v", *readings, screen.server.reading)
	}
	view := plain(screen.render())
	for _, want := range []string{"web-1", "2 ngày 3 giờ", "RAM", "sentinel.service", "đang chạy", "Không có unit nào lỗi"} {
		if !strings.Contains(view, want) {
			t.Errorf("the tab lacks %q:\n%s", want, view)
		}
	}
	if tab := screen.tabs()[serverTab]; tab.alarm || tab.count != 0 {
		t.Errorf("a healthy machine: alarm = %v, count = %d", tab.alarm, tab.count)
	}
	if _, cmd := send(t, screen, "1"); cmd != nil {
		t.Error("leaving the tab must not read the machine again")
	}
}

func TestServerTabReadsAgainOnRAndOnRefreshWhileOpenOnly(t *testing.T) {
	screen, readings := serverModel(t, healthyHost())
	screen = openServerTab(t, screen)

	screen, cmd := send(t, screen, "r")
	if cmd == nil || !screen.server.reading {
		t.Fatal("r must read again")
	}
	if again, second := send(t, screen, "r"); second != nil || !again.server.reading {
		t.Error("a second r while reading must not start another reading")
	}
	model, _ := screen.Update(cmd())
	screen = model.(manageModel)
	if *readings != 2 {
		t.Fatalf("readings = %d, want 2", *readings)
	}

	if _, refreshed := screen.refreshServer(); refreshed == nil {
		t.Error("the timed refresh must read while the tab is open")
	}
	screen, _ = send(t, screen, "1")
	if _, refreshed := screen.refreshServer(); refreshed != nil {
		t.Error("the timed refresh must not read while another tab is open")
	}
}

func TestServerTabColoursProblems(t *testing.T) {
	report := healthyHost()
	report.Load = [3]float64{9, 5, 3}      // 2.25 per CPU
	report.Memory.AvailableBytes = gib / 2 // 6% available
	report.Disks = append(report.Disks, host.Disk{Mount: "/srv", FSType: "xfs", FreeBytes: 3 * gib, TotalBytes: 100 * gib})
	report.Sentinel.Active = "inactive"
	report.FailedUnits = []host.Unit{{Name: "nginx.service", Active: "failed", Description: "web server"}}
	report.Errs = []error{errors.New("reading cpu: no")}
	screen, _ := serverModel(t, report)
	tab := openServerTab(t, screen).tabs()[serverTab]

	levelOf, reasonOf := map[string]severity{}, map[string]string{}
	for _, line := range tab.lines {
		levelOf[line.cells[0]], reasonOf[line.cells[0]] = line.severity, line.sub
	}
	want := map[string]severity{
		"CPU": sevBad, "RAM": sevWarn, "Đĩa /": sevOK, "Đĩa /srv": sevBad,
		"sentinel.service": sevWarn, "nginx.service": sevBad, "Không đọc được": sevWarn,
	}
	for name, level := range want {
		if got := levelOf[name]; got != level {
			t.Errorf("%s: severity %v, want %v", name, got, level)
		}
	}
	if !strings.Contains(reasonOf["Đĩa /srv"], "10%") || reasonOf["Đĩa /"] != "" {
		t.Errorf("only a problem explains itself, and a disk names its alert share: %q, %q", reasonOf["Đĩa /srv"], reasonOf["Đĩa /"])
	}
	if !tab.alarm || tab.count != 6 {
		t.Errorf("alarm = %v, count = %d, want an alarm and 6 problems", tab.alarm, tab.count)
	}
}

func TestServerTabDrawsUsageBars(t *testing.T) {
	screen, _ := serverModel(t, healthyHost())
	screen = openServerTab(t, screen)
	view := plain(screen.render())
	for _, want := range []string{"38%", "40%", "12%", "█", "░", "Đĩa /"} {
		if !strings.Contains(view, want) {
			t.Errorf("the tab lacks %q:\n%s", want, view)
		}
	}
	for _, line := range screen.tabs()[serverTab].lines {
		if line.cells[1] == "ok" {
			t.Errorf("%s: a healthy line says nothing, its dot and bar are green", line.cells[0])
		}
	}
}

func TestGaugeText(t *testing.T) {
	tests := []struct {
		percent float64
		width   int
		want    string
	}{
		{50, 20, "████████" + "░░░░░░░" + "  50%"}, // 15 cells of bar: 7.5 rounds to 8
		{0, 20, "░░░░░░░░░░░░░░░" + "   0%"},
		{100, 20, "███████████████" + " 100%"},
		{50, 25, "██████████" + "░░░░░░░░░░" + "  50%"},
		{100, 6, "█" + " 100%"}, // the narrowest column still shows a cell of bar
	}
	for _, tt := range tests {
		if got := plain(gaugeText(tt.percent, tt.width, sevOK, false)); got != tt.want {
			t.Errorf("gaugeText(%v, %d) = %q, want %q", tt.percent, tt.width, got, tt.want)
		}
	}
}

func TestServerTabWithoutSystemdHasNoServiceSection(t *testing.T) {
	report := healthyHost()
	report.Systemd = false
	screen, _ := serverModel(t, report)
	view := plain(openServerTab(t, screen).render())
	if strings.Contains(view, "DỊCH VỤ") || strings.Contains(view, "sentinel.service") {
		t.Errorf("a machine without systemd must not show services:\n%s", view)
	}
}

func TestDiskSeverity(t *testing.T) {
	tests := []struct {
		freePercent float64
		want        severity
	}{{50, sevOK}, {20, sevOK}, {19.9, sevWarn}, {10, sevWarn}, {9.9, sevBad}, {0, sevBad}}
	for _, tt := range tests {
		if got := diskSeverity(tt.freePercent, 10); got != tt.want {
			t.Errorf("diskSeverity(%v, 10) = %v, want %v", tt.freePercent, got, tt.want)
		}
	}
}

func TestUptimeText(t *testing.T) {
	tests := map[string]string{"45m": "45 phút", "5h10m": "5 giờ 10 phút", "51h": "2 ngày 3 giờ"}
	for input, want := range tests {
		duration, err := time.ParseDuration(input)
		if err != nil {
			t.Fatal(err)
		}
		if got := uptimeText(duration); got != want {
			t.Errorf("uptimeText(%s) = %q, want %q", input, got, want)
		}
	}
}
