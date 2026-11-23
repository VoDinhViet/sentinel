package main

import "fmt"

// keyHelp is one line of the key list.
type keyHelp struct{ key, does string }

// helpFor lists the keys that work on the screen the help was opened from.
func (m manageModel) helpFor() (title string, keys []keyHelp) {
	if m.helpReturn == modeSnapshots {
		return "Snapshots", []keyHelp{
			{"↑ ↓  j k", "select a snapshot"},
			{"enter  r", "restore the selected snapshot (asks for a directory, then the job name)"},
			{"click", "select a snapshot; click it again to restore"},
			{"wheel", "scroll"},
			{"esc  q", "back to the jobs"},
		}
	}
	common := []keyHelp{
		{"tab  1 2 3", "switch between Backups, Uptime and Alerts"},
		{"↑ ↓  j k", "select a line"},
		{"g  G", "first and last line"},
		{"r", "refresh now (the screen also refreshes every 5 seconds)"},
		{"click  wheel", "click a tab or a line; scroll with the wheel"},
		{"q  esc", "quit"},
	}
	if m.tab != 0 {
		return "Keys", common
	}
	return "Keys", append([]keyHelp{
		{"b", "back up the selected job now (ctrl+c cancels it)"},
		{"s  enter", "list the job's snapshots, then restore one"},
		{"a", "add a database job (postgres, mysql, mongodb) to the config"},
		{"e", "edit the selected database job"},
		{"d", "remove the selected database job from the config"},
	}, common...)
}

func (m manageModel) renderHelp() []string {
	title, keys := m.helpFor()
	screenLines := []string{titleStyle.Render(title), ""}
	for _, entry := range keys {
		screenLines = append(screenLines, fmt.Sprintf("  %s  %s", keyStyle.Render(fmt.Sprintf("%-12s", entry.key)), wrap(entry.does, max(m.width-18, 20))))
	}
	return append(screenLines, "", m.keyBar("any key", "close"))
}
