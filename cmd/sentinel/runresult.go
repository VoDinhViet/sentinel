package main

import "sentinel/internal/store"

// runResult is how a backup run reads in the CLI: "ok", "ok (warning: ...)"
// when the snapshot was made but retention failed, or "FAILED: ...".
func runResult(run store.BackupRun) string {
	switch {
	case !run.Success:
		return "FAILED: " + run.Error
	case run.Error != "":
		return "ok (warning: " + run.Error + ")"
	}
	return "ok"
}
