package agentapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanStripsControlCharactersFromEveryTextAndKeepsListsLists(t *testing.T) {
	const hostile = "a\x1b[31mb\x07"
	status := HostStatus{
		Hostname: hostile, OS: hostile, Kernel: hostile,
		Disks:      []DiskStatus{{Mount: hostile, Device: hostile, FSType: hostile}},
		Interfaces: []NetInterface{{Name: hostile}},
		TopProcesses: TopProcesses{
			ByCPU:    []Process{{Name: hostile}},
			ByMemory: []Process{{Name: hostile}},
		},
		Sentinel:       &ServiceStatus{Name: hostile, Active: hostile, Description: hostile},
		FailedServices: []ServiceStatus{{Name: hostile, Active: hostile, Description: hostile}},
		Errors:         []string{hostile},
	}
	body, err := json.Marshal(status.Clean())
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(body), "\x1b\x07") || strings.Contains(string(body), `\u001b`) || strings.Contains(string(body), `\u0007`) {
		t.Errorf("a control character is left: %s", body)
	}
	if status.Disks[0].Mount != hostile || status.TopProcesses.ByCPU[0].Name != hostile {
		t.Error("Clean changed the status it was given")
	}

	empty, err := json.Marshal(HostStatus{}.Clean())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"disks":[]`, `"interfaces":[]`, `"by_cpu":[]`, `"by_memory":[]`, `"failed_units":[]`, `"errors":[]`} {
		if !strings.Contains(string(empty), want) {
			t.Errorf("an empty status lacks %s: %s", want, empty)
		}
	}
}

// An older agent does not send the newer fields: reading its answer must still work.
func TestAStatusWithoutTheNewerFieldsStillDecodes(t *testing.T) {
	var status HostStatus
	if err := json.Unmarshal([]byte(`{"hostname":"web-01","cpus":2,"disks":[],"failed_units":[],"errors":[]}`), &status); err != nil {
		t.Fatal(err)
	}
	if status.Hostname != "web-01" || status.Interfaces != nil || status.TopProcesses.ByCPU != nil {
		t.Errorf("status = %+v", status)
	}
}
