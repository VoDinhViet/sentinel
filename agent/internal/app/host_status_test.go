package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/host"
)

func TestHostReportStatusCarriesEverythingAndCleansText(t *testing.T) {
	report := HostReport{MinFreePercent: 10, State: host.State{
		Hostname: "web-01", OS: "ubuntu 24.04", Uptime: 90 * time.Minute, CPUs: 4, Systemd: true,
		Sentinel:   host.Unit{Name: "sentinel.service", Active: "active"},
		Interfaces: []host.Interface{{Name: "eth0", RxBytes: 5, TxBytes: 6, RxErrors: 1, TxErrors: 2, RxDropped: 3, TxDropped: 4}},
		TopCPU:     []host.Process{{PID: 7, Name: "nginx\x1b[31m", CPUPercent: 50, MemoryBytes: 100}},
		TopMemory:  []host.Process{{PID: 8, Name: "java", CPUPercent: 1, MemoryBytes: 900}},
		Errs:       []error{errors.New("reading swap: unavailable")},
	}}
	status := report.Status()
	if status.UptimeSeconds != 5400 || status.MinFreePercent != 10 || status.Sentinel == nil {
		t.Errorf("status = %+v", status)
	}
	if len(status.Interfaces) != 1 {
		t.Fatalf("interfaces = %+v", status.Interfaces)
	}
	if got := status.Interfaces[0]; got.Name != "eth0" || got.RxBytes != 5 || got.TxBytes != 6 || got.RxErrors != 1 || got.TxErrors != 2 || got.RxDropped != 3 || got.TxDropped != 4 {
		t.Errorf("interface = %+v", got)
	}
	if got := status.TopProcesses.ByCPU; len(got) != 1 || got[0].Name != "nginx" || got[0].PID != 7 {
		t.Errorf("by cpu = %+v, want nginx cleaned of its escape sequence", got)
	}
	if got := status.TopProcesses.ByMemory; len(got) != 1 || got[0].MemoryBytes != 900 {
		t.Errorf("by memory = %+v", got)
	}
	if len(status.Errors) != 1 || !strings.Contains(status.Errors[0], "swap") {
		t.Errorf("errors = %v", status.Errors)
	}
}

func TestHostReportStatusOmitsSentinelWithoutSystemd(t *testing.T) {
	if status := (HostReport{}).Status(); status.Sentinel != nil || status.Interfaces == nil || status.TopProcesses.ByCPU == nil {
		t.Errorf("status = %+v, want no sentinel unit and lists that are empty, not null", status)
	}
}
