// Package engine is the one place that knows how each database engine is
// dumped, which tools it needs and how its dump is loaded back. Callers ask
// For(jobType) instead of branching on the engine name.
package engine

import (
	"sentinel/internal/config"
)

// Dump is how to produce one logical dump of a database on stdout.
type Dump struct {
	Command  []string // argv of the dump tool, writing the dump to stdout
	Filename string   // name the dump is stored under inside the snapshot
	Env      []string // extra environment ("KEY=value"), e.g. the password
}

// Engine describes one database engine.
type Engine interface {
	// Type is the backup job type this engine handles ("postgres").
	Type() string
	// Tools are the binaries a host must have to back this engine up.
	Tools() []string
	// Database is the name of the database the job dumps.
	Database(job *config.Backup) string
	// Dump builds the dump invocation for a job.
	Dump(job *config.Backup) (Dump, error)
	// RestoreHint is the shell command that loads a restored dump back in;
	// dir is where the snapshot was restored to.
	RestoreHint(job *config.Backup, dir string) string
}

var engines = map[string]Engine{}

func register(e Engine) { engines[e.Type()] = e }

func init() { register(postgres{}) }

// For returns the engine for a backup job type, if the type is a database one
// (not "files").
func For(jobType string) (Engine, bool) {
	e, ok := engines[jobType]
	return e, ok
}

// IsDatabase reports whether a backup job type is a database engine.
func IsDatabase(jobType string) bool {
	_, ok := engines[jobType]
	return ok
}

// ToolsFor returns the distinct binaries needed by the database jobs among
// backups, in first-seen order.
func ToolsFor(backups []config.Backup) []string {
	var tools []string
	seen := map[string]bool{}
	for i := range backups {
		e, ok := For(backups[i].Type)
		if !ok {
			continue
		}
		for _, tool := range e.Tools() {
			if !seen[tool] {
				seen[tool] = true
				tools = append(tools, tool)
			}
		}
	}
	return tools
}
