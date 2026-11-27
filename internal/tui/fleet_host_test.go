package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sentinel/internal/fleet"
)

// machineOfAServer is a reading of a machine with a busy disk and a service that failed.
func machineOfAServer() fleet.HostStatus {
	return fleet.HostStatus{
		Hostname: "db-01", OS: "debian 12", Kernel: "6.1.0", UptimeSeconds: 3 * 24 * 3600, Load: [3]float64{0.5, 0.4, 0.3}, CPUs: 4, CPUPercent: 12,
		Memory:         fleet.MemoryStatus{TotalBytes: 8 * gib, AvailableBytes: 5 * gib, SwapTotalBytes: 2 * gib, SwapFreeBytes: 2 * gib},
		Disks:          []fleet.DiskStatus{{Mount: "/var/backups", Device: "/dev/sdb1", FSType: "xfs", FreeBytes: 3 * gib, TotalBytes: 100 * gib}},
		Systemd:        true,
		Sentinel:       &fleet.ServiceStatus{Name: "sentinel.service", Active: "active"},
		FailedServices: []fleet.ServiceStatus{{Name: "nginx.service", Active: "failed", Description: "web server"}},
		MinFreePercent: 10,
		Errors:         []string{"reading swap: unavailable"},
	}
}

// openServerScreenWithMachine opens the screen of db-01 with a host action that counts its readings.
func openServerScreenWithMachine(t *testing.T, machine fleet.HostStatus, err error) (manageModel, *[]string) {
	t.Helper()
	readings := new([]string)
	screen, _ := fleetModel(t, fleetWithDetails(), nil)
	screen.actions.fleetHost = func(_ context.Context, server fleet.Server) (fleet.HostStatus, error) {
		*readings = append(*readings, server.Name)
		return machine, err
	}
	screen = openFleetTab(t, screen)
	screen = keys(t, screen, "down")
	model, cmd := screen.onKey("enter", "")
	screen = model.(manageModel)
	if cmd == nil || !screen.fleet.host.reading {
		t.Fatal("opening the screen of a server must read its machine")
	}
	model, _ = screen.Update(cmd())
	return model.(manageModel), readings
}

func TestTheMachinePartShowsWhatTheServerReports(t *testing.T) {
	screen, readings := openServerScreenWithMachine(t, machineOfAServer(), nil)
	if strings.Join(*readings, ",") != "db-01" {
		t.Fatalf("machines read = %v, want db-01 alone", *readings)
	}
	view := plain(keys(t, screen, "4").render())
	for _, want := range []string{"db-01", "debian 12", "3 ngày", "CPU", "RAM", "Swap", "/var/backups", "sentinel.service", "nginx.service", "reading swap: unavailable"} {
		if !strings.Contains(view, want) {
			t.Errorf("the Máy part lacks %q:\n%s", want, view)
		}
	}
	tab := screen.top().(fleetServerScreen).parts(screen, fleet.ServerStatus{})[serverHostPart]
	if tab.title != "Máy" || !tab.alarm {
		t.Errorf("title %q, alarm %v: a disk 97%% full and a failed unit must colour the pill red", tab.title, tab.alarm)
	}
}

func TestTheMachinePartSaysWhyItIsEmpty(t *testing.T) {
	waiting, _ := fleetModel(t, fleetWithDetails(), nil)
	waiting.actions.fleetHost = func(context.Context, fleet.Server) (fleet.HostStatus, error) { return fleet.HostStatus{}, nil }
	waiting = openFleetTab(t, waiting)
	waiting, _ = waiting.openFleetServer()
	if view := plain(keys(t, waiting, "4").render()); !strings.Contains(view, "Đang đọc tình trạng máy") {
		t.Errorf("no progress text while reading:\n%s", view)
	}

	old, _ := openServerScreenWithMachine(t, fleet.HostStatus{}, fmt.Errorf("server db-01: %w", fleet.ErrNotSupported))
	if view := plain(keys(t, old, "4").render()); !strings.Contains(view, "bản Sentinel cũ") {
		t.Errorf("no hint to update an old server:\n%s", view)
	}
	failed, _ := openServerScreenWithMachine(t, fleet.HostStatus{}, errors.New("boom"))
	if view := plain(keys(t, failed, "4").render()); !strings.Contains(view, "boom") {
		t.Errorf("the reason is not shown:\n%s", view)
	}
}

func TestTheMachineIsReadAgainWithTheRoundAndNeverForTheList(t *testing.T) {
	screen, readings := openServerScreenWithMachine(t, machineOfAServer(), nil)
	next, cmd := screen.refreshFleet()
	if cmd == nil || !next.fleet.reading || !next.fleet.host.reading {
		t.Fatal("the timed refresh must ask the servers and read the open server's machine")
	}
	if _, again := next.refreshFleetHost(); again != nil {
		t.Error("a second reading must not start while one is under way")
	}

	list := keys(t, screen, "esc")
	if _, cmd := list.refreshFleetHost(); cmd != nil {
		t.Error("the machine of a server only listed must not be read")
	}
	if len(*readings) != 1 {
		t.Errorf("machines read = %v", *readings)
	}
}

func TestTheMachineOfAnotherServerNeverStandsInForThisOne(t *testing.T) {
	screen, _ := openServerScreenWithMachine(t, machineOfAServer(), nil)
	late, _ := screen.Update(fleetHostReadMsg{server: "web-01", host: fleet.HostStatus{Hostname: "web-01"}})
	if got := late.(manageModel).fleet.host.status.Hostname; got != "db-01" {
		t.Errorf("a late answer of web-01 replaced the machine: %q", got)
	}

	other, _ := fleetModel(t, fleetWithDetails(), nil)
	other.actions.fleetHost = func(context.Context, fleet.Server) (fleet.HostStatus, error) { return machineOfAServer(), nil }
	other.fleet.host = fleetHostState{server: "db-01", read: true, status: machineOfAServer()}
	other = openFleetTab(t, other)
	other, _ = other.startFleetHost("web-01")
	if other.fleet.host.read || other.fleet.host.server != "web-01" {
		t.Errorf("the numbers of db-01 are still there while web-01 is read: %+v", other.fleet.host)
	}
}

func TestTheMachineOfAnUnknownServerIsNotRead(t *testing.T) {
	screen, _ := fleetModel(t, fleetWithDetails(), nil)
	screen.actions.fleetHost = func(context.Context, fleet.Server) (fleet.HostStatus, error) {
		t.Error("read the machine of a server that is not in the fleet")
		return fleet.HostStatus{}, nil
	}
	screen = openFleetTab(t, screen)
	if _, cmd := screen.startFleetHost("nobody"); cmd != nil {
		t.Error("a command for a server nobody listed")
	}
}
