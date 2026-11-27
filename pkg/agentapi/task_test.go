package agentapi

import (
	"strings"
	"testing"
)

func TestValidateTaskAcceptsTheTableAndNothingElse(t *testing.T) {
	good := []struct {
		action string
		params map[string]string
	}{
		{ActionBackupStart, map[string]string{"job": "db"}},
		{ActionBackupStart, map[string]string{"job": "db", "repository": "s3"}},
		{ActionBackupStop, map[string]string{"job": "web files"}},
		{ActionBackupSnapshots, map[string]string{"job": "db"}},
		{ActionDoctorRun, nil},
		{ActionDoctorRun, map[string]string{}},
		{ActionBackupStart, map[string]string{"job": "sao lưu cơ sở dữ liệu"}},
	}
	for _, tc := range good {
		if err := ValidateTask(tc.action, tc.params); err != nil {
			t.Errorf("%s %v refused: %v", tc.action, tc.params, err)
		}
	}
	bad := []struct {
		name, action string
		params       map[string]string
		want         string
	}{
		{"an action that is not in the table", "service.restart", map[string]string{"unit": "nginx.service"}, "unknown action"},
		{"a shell", "shell.run", map[string]string{"cmd": "id"}, "unknown action"},
		{"no action", "", nil, "unknown action"},
		{"a missing parameter", ActionBackupStart, nil, "needs the parameter job"},
		{"an empty parameter", ActionBackupStart, map[string]string{"job": ""}, "empty"},
		{"a parameter the action does not take", ActionDoctorRun, map[string]string{"path": "/etc"}, "does not take"},
		{"a path smuggled in as a second parameter", ActionBackupStart, map[string]string{"job": "db", "path": "/etc"}, "does not take"},
		{"a line break", ActionBackupStart, map[string]string{"job": "db\nx"}, "control character"},
		{"an escape sequence", ActionBackupStart, map[string]string{"job": "db\x1b[2J"}, "control character"},
		{"a long value", ActionBackupStart, map[string]string{"job": strings.Repeat("a", 129)}, "longer than"},
	}
	for _, tc := range bad {
		err := ValidateTask(tc.action, tc.params)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one with %q", tc.name, err, tc.want)
		}
	}
}

func TestAnErrorDoesNotRepeatAnEscapeSequenceToTheTerminal(t *testing.T) {
	err := ValidateTask("x\x1b[2Jy", nil)
	if err == nil || strings.ContainsRune(err.Error(), 0x1b) {
		t.Errorf("err = %q", err)
	}
	err = ValidateTask(ActionDoctorRun, map[string]string{"a\x1b[2J": "v"})
	if err == nil || strings.ContainsRune(err.Error(), 0x1b) {
		t.Errorf("err = %q", err)
	}
}

func TestEveryActionHasATimeoutAndTheTableIsSorted(t *testing.T) {
	names := Actions()
	for index, name := range names {
		if Timeout(name) <= 0 {
			t.Errorf("%s has no timeout", name)
		}
		if index > 0 && names[index-1] >= name {
			t.Errorf("actions are not in order: %v", names)
		}
	}
	if Timeout("nope") != 0 {
		t.Error("an unknown action has a timeout")
	}
}

func TestOnlyTheThreeEndStatesAreAgentChoices(t *testing.T) {
	for state, want := range map[string]bool{
		TaskSucceeded: true, TaskFailed: true, TaskRefused: true,
		TaskQueued: false, TaskSent: false, TaskCanceled: false, TaskExpired: false, "": false, "whatever": false,
	} {
		if ValidState(state) != want {
			t.Errorf("ValidState(%q) = %v", state, !want)
		}
	}
	if Finished(TaskQueued) || Finished(TaskSent) || !Finished(TaskExpired) || !Finished(TaskSucceeded) {
		t.Error("Finished is wrong")
	}
}

func TestResultCleanDropsEscapesKeepsLinesAndCuts(t *testing.T) {
	cleaned := TaskResult{State: TaskFailed, Output: "a\x1b[31mb\nc", Error: "x\ny" + strings.Repeat("z", MaxTaskError)}.Clean()
	if cleaned.Output != "ab\nc" {
		t.Errorf("output = %q", cleaned.Output)
	}
	if strings.Contains(cleaned.Error, "\n") || len([]rune(cleaned.Error)) > MaxTaskError {
		t.Errorf("error is not one short line: %d runes", len([]rune(cleaned.Error)))
	}
	big := TaskResult{Output: strings.Repeat("é", MaxTaskOutput*2)}.Clean()
	if len([]rune(big.Output)) != MaxTaskOutput {
		t.Errorf("output has %d runes", len([]rune(big.Output)))
	}
}

func TestAResultAndALogAreMaskedEvenWhenTheAgentDidNotMaskThem(t *testing.T) {
	result := TaskResult{State: TaskFailed, Output: "repo s3:https://user:hunter2@host/bucket", Error: "AWS_SECRET_ACCESS_KEY=abcd1234 was refused"}.Clean()
	for _, text := range []string{result.Output, result.Error, CleanText("password=hunter2", 100)} {
		if strings.Contains(text, "hunter2") || strings.Contains(text, "abcd1234") {
			t.Errorf("a secret is still in %q", text)
		}
	}
}
