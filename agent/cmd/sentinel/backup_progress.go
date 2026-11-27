package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"sentinel/agent/internal/backup"
)

func stderrIsTerminal() bool {
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// progressInterval is how often a progress line goes to a log or a pipe; a terminal gets one
// line that is rewritten in place every time restic reports.
const progressInterval = 30 * time.Second

// progressPrinter shows how far a backup has got on stderr: one line rewritten in place on a
// terminal, a new line every 30 seconds (and at each phase) anywhere else, such as a log file.
type progressPrinter struct {
	out   io.Writer
	tty   bool
	now   func() time.Time
	mu    sync.Mutex
	last  time.Time
	phase string
	shown bool
}

func newProgressPrinter(out io.Writer, tty bool, now func() time.Time) *progressPrinter {
	return &progressPrinter{out: out, tty: tty, now: now}
}

func (p *progressPrinter) show(progress backup.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := fmt.Sprintf("%s → %s: %s", progress.Job, progress.Repository, progress.Summary())
	if p.tty {
		fmt.Fprintf(p.out, "\r\033[K%s", line)
		p.shown = true
		return
	}
	if now := p.now(); progress.Phase == p.phase && now.Sub(p.last) < progressInterval {
		return
	}
	p.last, p.phase = p.now(), progress.Phase
	fmt.Fprintln(p.out, line)
}

// done ends the rewritten line so the result table starts on a line of its own.
func (p *progressPrinter) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tty && p.shown {
		fmt.Fprint(p.out, "\r\033[K")
	}
}
