package daemon

import "testing"

// The history is pruned when the daemon starts, not only 24 hours later: a daemon that is restarted
// more often than that (every change of the config asks for it) would otherwise never prune.
func TestPruneHistoryRunsAtStart(t *testing.T) {
	for _, task := range (&Daemon{Alerts: nil}).builtinTasks() {
		if task.Name == "prune-history" {
			if !task.AtStart {
				t.Error("prune-history does not run at start")
			}
			return
		}
	}
	t.Error("no prune-history task")
}
