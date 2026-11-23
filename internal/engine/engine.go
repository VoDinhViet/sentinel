// Package engine is the one place that knows how each database engine is
// dumped, which tools it needs and how its dump is loaded back. Callers ask
// For(jobType) instead of branching on the engine name.
package engine

import (
	"maps"
	"os"
	"slices"

	"sentinel/internal/config"
)

// Dump is how to produce one logical dump of a database on stdout.
type Dump struct {
	Command  []string // argv of the dump tool, writing the dump to stdout
	Filename string   // name the dump is stored under inside the snapshot
	Env      []string // extra environment ("KEY=value"), e.g. the password
	// Cleanup removes what Dump created for the run (a temporary credentials
	// file). It is nil when there is nothing to remove, and safe to call twice.
	Cleanup func()
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

func init() {
	register(postgres{})
	register(mysql{})
	register(mongodb{})
}

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

// Capabilities lists what this agent build supports ("engine:postgres"), so
// the server can refuse a config the agent would reject.
func Capabilities() []string {
	var capabilities []string
	for _, jobType := range slices.Sorted(maps.Keys(engines)) {
		capabilities = append(capabilities, "engine:"+jobType)
	}
	return capabilities
}

// writeSecretFile writes content to a new file readable only by the current
// user, so a password never appears in a process's argv. The caller removes it.
func writeSecretFile(pattern, content string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", pattern) // created with mode 0600
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.Remove(f.Name()) }
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}
