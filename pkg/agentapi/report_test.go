package agentapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReportCleanStripsTextCutsListsAndKeepsThemLists(t *testing.T) {
	const hostile = "a\x1b[31mb\x07"
	report := Report{
		Version: hostile,
		Backups: []BackupStatus{{Job: hostile, Repository: hostile, Result: hostile}},
		Checks:  []CheckStatus{{Name: hostile, Error: hostile}},
		Alerts:  []AlertStatus{{Key: hostile, Message: strings.Repeat("x", 5000)}},
	}
	for range MaxReportItems + 50 {
		report.Checks = append(report.Checks, CheckStatus{Name: "c"})
	}
	cleaned := report.Clean()
	body, err := json.Marshal(cleaned)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(body), "\x1b\x07") || strings.Contains(string(body), `\u001b`) {
		t.Errorf("a control character is left: %s", body)
	}
	if len(cleaned.Checks) != MaxReportItems {
		t.Errorf("checks = %d, want %d", len(cleaned.Checks), MaxReportItems)
	}
	if got := len([]rune(cleaned.Alerts[0].Message)); got > maxReportText+1 {
		t.Errorf("an alert message of %d runes was not cut", got)
	}
	if report.Backups[0].Job != hostile {
		t.Error("Clean changed the report it was given")
	}
	empty, _ := json.Marshal(Report{}.Clean())
	for _, want := range []string{`"backups":[]`, `"checks":[]`, `"alerts":[]`} {
		if !strings.Contains(string(empty), want) {
			t.Errorf("an empty report lacks %s: %s", want, empty)
		}
	}
}
