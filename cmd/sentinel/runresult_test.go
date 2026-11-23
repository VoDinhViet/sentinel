package main

import (
	"testing"

	"sentinel/internal/store"
)

func TestRunResult(t *testing.T) {
	for name, tc := range map[string]struct {
		run  store.BackupRun
		want string
	}{
		"success":              {store.BackupRun{Success: true}, "ok"},
		"success with warning": {store.BackupRun{Success: true, Error: "retention failed: locked"}, "ok (warning: retention failed: locked)"},
		"failure":              {store.BackupRun{Error: "disk on fire"}, "FAILED: disk on fire"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := runResult(tc.run); got != tc.want {
				t.Errorf("runResult = %q, want %q", got, tc.want)
			}
		})
	}
}
