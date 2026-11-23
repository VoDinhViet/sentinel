// Package engine is the one place that knows how each database engine is
// dumped, which tools it needs and how its dump is loaded back. Callers ask
// For(jobType) instead of branching on the engine name.
package engine

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

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
	Database(job *config.Job) string
	// Dump builds the dump invocation for a job.
	Dump(job *config.Job) (Dump, error)
	// RestoreHint is the shell command that loads a restored dump back in;
	// dir is where the snapshot was restored to.
	RestoreHint(job *config.Job, dir string) string
}

var engines = map[string]Engine{}

func register(dbEngine Engine) { engines[dbEngine.Type()] = dbEngine }

func init() {
	register(postgres{})
	register(mysql{})
	register(mongodb{})
}

// For returns the engine for a backup job type, if the type is a database one
// (not "files").
func For(jobType string) (Engine, bool) {
	dbEngine, ok := engines[jobType]
	return dbEngine, ok
}

// IsDatabase reports whether a backup job type is a database engine.
func IsDatabase(jobType string) bool {
	_, ok := engines[jobType]
	return ok
}

// ToolsFor returns the distinct binaries needed by the database jobs among
// jobs, in first-seen order.
func ToolsFor(jobs []config.Job) []string {
	var tools []string
	seenTools := map[string]bool{}
	for i := range jobs {
		dbEngine, ok := For(jobs[i].Type)
		if !ok {
			continue
		}
		for _, tool := range dbEngine.Tools() {
			if !seenTools[tool] {
				seenTools[tool] = true
				tools = append(tools, tool)
			}
		}
	}
	return tools
}

// Capabilities lists the engine job types this build supports ("engine:postgres").
func Capabilities() []string {
	var capabilities []string
	for _, jobType := range slices.Sorted(maps.Keys(engines)) {
		capabilities = append(capabilities, "engine:"+jobType)
	}
	return capabilities
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote quotes value for a POSIX shell, leaving plain words as they are.
// It is only used for the restore hints that are printed for people to copy.
func shellQuote(value string) string {
	if shellSafe.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// secretFilePatterns are the temporary credential files engines create.
var secretFilePatterns = []string{"sentinel-mysql-*.cnf", "sentinel-mongo-*.yaml"}

// SweepStaleSecretFiles removes credential files left behind by a crashed or
// killed dump. olderThan must exceed the longest backup timeout so a file in
// use by a running dump is never removed. It returns how many were removed.
func SweepStaleSecretFiles(olderThan time.Duration) int {
	removed := 0
	for _, pattern := range secretFilePatterns {
		secretPaths, _ := filepath.Glob(filepath.Join(os.TempDir(), pattern))
		for _, path := range secretPaths {
			fileInfo, err := os.Lstat(path)
			if err != nil || !fileInfo.Mode().IsRegular() || time.Since(fileInfo.ModTime()) < olderThan {
				continue
			}
			if os.Remove(path) == nil {
				removed++
			}
		}
	}
	return removed
}

// writeSecretFile writes content to a new file readable only by the current
// user, so a password never appears in a process's argv. The caller removes it.
func writeSecretFile(pattern, content string) (path string, cleanup func(), err error) {
	file, err := os.CreateTemp("", pattern) // created with mode 0600
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.Remove(file.Name()) }
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return file.Name(), cleanup, nil
}
