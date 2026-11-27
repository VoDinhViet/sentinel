package host

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSkipInterface(t *testing.T) {
	for name, want := range map[string]bool{
		"lo": true, "veth1a2b3c": true, "vethXYZ": true,
		"eth0": false, "ens3": false, "docker0": false, "wg0": false, "lo2": false,
	} {
		if got := skipInterface(name); got != want {
			t.Errorf("skipInterface(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRankProcesses(t *testing.T) {
	all := []Process{
		{PID: 1, Name: "idle", CPUPercent: 0, MemoryBytes: 10},
		{PID: 2, Name: "busy", CPUPercent: 90, MemoryBytes: 20},
		{PID: 3, Name: "big", CPUPercent: 5, MemoryBytes: 900},
		{PID: 4, Name: "mid", CPUPercent: 40, MemoryBytes: 300},
		{PID: 5, Name: "small", CPUPercent: 1, MemoryBytes: 5},
	}
	byCPU, byMemory := rankProcesses(all, 2)
	if len(byCPU) != 2 || byCPU[0].Name != "busy" || byCPU[1].Name != "mid" {
		t.Errorf("by cpu = %+v, want busy then mid", byCPU)
	}
	if len(byMemory) != 2 || byMemory[0].Name != "big" || byMemory[1].Name != "mid" {
		t.Errorf("by memory = %+v, want big then mid", byMemory)
	}
	if all[0].Name != "idle" || all[1].Name != "busy" {
		t.Error("ranking reordered the list it was given")
	}

	// A machine where nothing runs lists no busy process, not n arbitrary ones.
	byCPU, byMemory = rankProcesses([]Process{{PID: 1, Name: "a"}, {PID: 2, Name: "b"}}, 5)
	if len(byCPU) != 0 || len(byMemory) != 2 {
		t.Errorf("idle machine: by cpu %+v, by memory %+v", byCPU, byMemory)
	}
	if byCPU, byMemory = rankProcesses(nil, 5); len(byCPU) != 0 || len(byMemory) != 0 {
		t.Error("no processes must rank to nothing")
	}
}

func TestCPUShare(t *testing.T) {
	key := processKey{pid: 7, started: 1}
	tests := []struct {
		name          string
		used          float64
		previous      map[processKey]float64
		sincePrevious time.Duration
		sinceStart    time.Duration
		want          float64
	}{
		{"since the previous reading", 12, map[processKey]float64{key: 10}, 10 * time.Second, time.Hour, 20},
		{"two busy cpus", 30, map[processKey]float64{key: 10}, 10 * time.Second, time.Hour, 200},
		{"first reading is since the start", 5, nil, 0, 100 * time.Second, 5},
		{"a new process is since its start", 5, map[processKey]float64{{pid: 8, started: 1}: 1}, 10 * time.Second, 50 * time.Second, 10},
		{"a counter that went back is not a negative share", 1, map[processKey]float64{key: 10}, 10 * time.Second, time.Hour, 0},
		{"no time passed", 5, nil, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cpuShare(tt.used, tt.previous, key, tt.sincePrevious, tt.sinceStart); got != tt.want {
				t.Errorf("cpuShare = %v, want %v", got, tt.want)
			}
		})
	}
}

// On the real machine: the lists are bounded, sorted, and never carry what a process was started with.
func TestReadSeesProcessesAndInterfaces(t *testing.T) {
	ctx := context.Background()
	state := Read(ctx)
	if len(state.TopMemory) == 0 || len(state.TopMemory) > topCount || len(state.TopCPU) > topCount {
		t.Fatalf("top memory %d, top cpu %d", len(state.TopMemory), len(state.TopCPU))
	}
	for i := 1; i < len(state.TopMemory); i++ {
		if state.TopMemory[i].MemoryBytes > state.TopMemory[i-1].MemoryBytes {
			t.Errorf("by memory is not sorted: %+v", state.TopMemory)
		}
	}
	for _, found := range state.Interfaces {
		if skipInterface(found.Name) {
			t.Errorf("interface %q should have been left out", found.Name)
		}
	}
}

// A process started with a secret in its arguments and its environment shows only its name.
func TestAProcessesArgumentsAndEnvironmentAreNotRead(t *testing.T) {
	const secret = "s3cr3t-token-for-test"
	cmd := exec.Command("sleep", "30", secret)
	cmd.Env = append(os.Environ(), "SENTINEL_TEST_SECRET="+secret)
	if err := cmd.Start(); err != nil {
		t.Skip("sleep is not available:", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	all, err := collectProcesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, proc := range all {
		if int(proc.PID) == cmd.Process.Pid {
			found = true
			if proc.Name != "sleep" {
				t.Errorf("name = %q, want sleep", proc.Name)
			}
		}
		if strings.Contains(proc.Name, secret) {
			t.Errorf("a process name holds the secret: %+v", proc)
		}
	}
	if !found {
		t.Error("the process that was started is not listed")
	}
}
