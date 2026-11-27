package executor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sentinel/pkg/agentapi"
)

// What the two OS actions do, and nothing else. Which packages are upgraded is apt's own decision: no task
// names a package, a source, an option or a reboot, and the arguments below are all there is.
const (
	// updateUnit is the transient unit an upgrade runs in. A second upgrade finds the name taken and fails,
	// which is the lock against two at once; it is also the name to look at (journalctl -u) if one goes wrong.
	updateUnit = "sentinel-os-update"
	// maxUpdateLines is how many packages a check lists; maxScanLines is how much of apt's output is kept.
	maxUpdateLines = 100
	maxScanLines   = 5000
	tailLines      = 40
)

// toolDirs are where apt-get and systemd-run are looked for: fixed places, not the PATH of the daemon.
var toolDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// rebootRequiredFile is what Debian and Ubuntu create when an upgrade needs a restart; listsStampFile is touched
// by every successful `apt-get update`, so its age is how stale the package lists are.
var (
	rebootRequiredFile = "/run/reboot-required"
	listsStampFile     = "/var/lib/apt/periodic/update-success-stamp"
)

// lookTool finds a program in toolDirs.
func lookTool(name string) (string, error) {
	for _, dir := range toolDirs {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("this machine has no %s (only Debian and Ubuntu are supported)", name)
}

// runTool runs the program at path with exactly these arguments, in a small environment of its own, and
// gives each line it prints (both streams together) to line, here in the calling goroutine. Cancelling ctx
// kills the program and stops the reading, even when a child of it still holds the pipe.
func runTool(ctx context.Context, path string, args []string, line func(string)) error {
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // path comes from toolDirs; the arguments are built in this file
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "DEBIAN_FRONTEND=noninteractive"}
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { out.Close() })
	defer stop()
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64<<10), 256<<10)
	for scanner.Scan() {
		line(scanner.Text())
	}
	return errors.Join(cmd.Wait(), ctx.Err())
}

func (e *Executor) lookTool(name string) (string, error) {
	if e.LookTool != nil {
		return e.LookTool(name)
	}
	return lookTool(name)
}

func (e *Executor) runTool(ctx context.Context, path string, args []string, line func(string)) error {
	if e.RunTool != nil {
		return e.RunTool(ctx, path, args, line)
	}
	return runTool(ctx, path, args, line)
}

// instLine is a line of `apt-get -s` that says a package would be installed or upgraded:
// Inst libc6 [2.35-1] (2.35-2 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])
var instLine = regexp.MustCompile(`^Inst (\S+)(?: \[([^\]]*)\])? \((\S+) ([^)]*)\)`)

// upgrade is one package a simulation would upgrade.
type upgrade struct {
	name, from, to string
	security       bool
}

// parseSimulation reads the output of `apt-get -s upgrade`.
func parseSimulation(lines []string) (upgrades []upgrade, keptBack []string, summary string) {
	inKept := false
	for _, text := range lines {
		if match := instLine.FindStringSubmatch(text); match != nil {
			upgrades = append(upgrades, upgrade{name: match[1], from: match[2], to: match[3], security: strings.Contains(strings.ToLower(match[4]), "-security")})
			inKept = false
			continue
		}
		switch {
		case strings.HasPrefix(text, "The following packages have been kept back:"):
			inKept = true
		case inKept && strings.HasPrefix(text, "  "):
			keptBack = append(keptBack, strings.Fields(text)...)
		case upgraded.MatchString(text):
			summary, inKept = text, false
		default:
			inKept = false
		}
	}
	return upgrades, keptBack, summary
}

// checkUpdates lists what `apt-get -s upgrade` says it would upgrade. It changes nothing: -s is a simulation.
func (e *Executor) checkUpdates(ctx context.Context, log func(string)) agentapi.TaskResult {
	apt, err := e.lookTool("apt-get")
	if err != nil {
		return failed(err)
	}
	log("apt-get -s upgrade")
	var lines []string
	err = e.runTool(ctx, apt, []string{"-s", "-o", "Debug::NoLocking=1", "upgrade"}, func(text string) {
		if len(lines) < maxScanLines {
			lines = append(lines, text)
		}
	})
	if err != nil {
		return failed(fmt.Errorf("apt-get -s upgrade: %w: %s", err, lastLine(lines)))
	}
	upgrades, keptBack, summary := parseSimulation(lines)
	security := 0
	for _, found := range upgrades {
		if found.security {
			security++
		}
	}
	report := []string{fmt.Sprintf("%d packages can be upgraded, %d of them security updates", len(upgrades), security)}
	if summary != "" {
		report = append(report, summary)
	}
	if len(keptBack) > 0 {
		report = append(report, fmt.Sprintf("%d kept back (an upgrade would have to add or remove packages): %s", len(keptBack), strings.Join(keptBack[:min(len(keptBack), 20)], " ")))
	}
	for _, found := range upgrades[:min(len(upgrades), maxUpdateLines)] {
		mark := ""
		if found.security {
			mark = "  [security]"
		}
		report = append(report, fmt.Sprintf("  %s %s -> %s%s", found.name, found.from, found.to, mark))
	}
	if len(upgrades) > maxUpdateLines {
		report = append(report, fmt.Sprintf("  … and %d more", len(upgrades)-maxUpdateLines))
	}
	return succeeded(strings.Join(append(report, e.stateNotes()...), "\n"))
}

// stateNotes say how fresh the package lists are and whether the machine is waiting for a restart.
func (e *Executor) stateNotes() []string {
	var notes []string
	if info, err := os.Stat(listsStampFile); err == nil {
		notes = append(notes, fmt.Sprintf("package lists updated %s ago (apt-get update was last successful then)", e.since(info.ModTime()).Round(time.Minute)))
	}
	if _, err := os.Stat(rebootRequiredFile); err == nil {
		notes = append(notes, "a restart of this machine is required (sentinel never restarts it)")
	}
	return notes
}

func (e *Executor) since(then time.Time) time.Duration { return e.now().Sub(then) }

// applyUpdates upgrades the packages of this machine with `apt-get -y upgrade`, in the transient unit
// sentinel-os-update. The daemon's own sandbox (ProtectSystem=full) keeps /usr read-only, so the upgrade has to
// run outside it: systemd starts the unit, and that is the one deliberate way out of the sandbox. There is no
// package name, no dist-upgrade, no source change and no reboot, and an upgrade already running blocks a second.
func (e *Executor) applyUpdates(ctx context.Context, log func(string)) agentapi.TaskResult {
	apt, err := e.lookTool("apt-get")
	if err != nil {
		return failed(err)
	}
	run, err := e.lookTool("systemd-run")
	if err != nil {
		return failed(err)
	}
	args := []string{"--wait", "--pipe", "--collect", "--quiet", "--unit=" + updateUnit,
		"--setenv=DEBIAN_FRONTEND=noninteractive", "--setenv=LANG=C",
		"--", apt, "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "upgrade"}
	log("upgrading the packages with apt-get in the unit " + updateUnit)
	var tail []string
	summary := ""
	err = e.runTool(ctx, run, args, func(text string) {
		log(text)
		if tail = append(tail, text); len(tail) > tailLines {
			tail = tail[1:]
		}
		if upgraded.MatchString(text) {
			summary = text
		}
	})
	output := strings.Join(tail, "\n")
	if err != nil {
		note := ""
		if ctx.Err() != nil {
			note = fmt.Sprintf(" (stopped waiting: the upgrade goes on in the unit %s, see journalctl -u %s)", updateUnit, updateUnit)
		}
		result := failed(fmt.Errorf("the upgrade did not finish well: %w: %s%s", err, lastLine(tail), note))
		result.Output = output
		return result
	}
	if summary == "" {
		summary = "the upgrade finished"
	}
	return succeeded(strings.Join(append([]string{summary}, append(e.stateNotes(), "", "the last lines of apt:", output)...), "\n"))
}

var upgraded = regexp.MustCompile(`^\d+ upgraded, \d+ newly installed`)

func lastLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if text := strings.TrimSpace(lines[i]); text != "" {
			return text
		}
	}
	return ""
}
