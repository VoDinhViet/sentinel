package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"sentinel/agent/internal/backup"
)

func TestProgressPrinterWritesALineEvery30SecondsOnAPipe(t *testing.T) {
	var out bytes.Buffer
	now := time.Unix(1_000_000, 0)
	printer := newProgressPrinter(&out, false, func() time.Time { return now })
	progress := backup.Progress{Job: "shop", Repository: "local", Phase: backup.PhaseBackup, BytesDone: 5 << 30, BytesTotal: 20 << 30, Percent: 25, Estimated: true}
	printer.show(progress)
	now = now.Add(2 * time.Second)
	printer.show(progress) // too soon
	now = now.Add(29 * time.Second)
	printer.show(progress)
	progress.Phase = backup.PhaseRetention
	printer.show(progress) // a new phase is always said
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "shop → local: ~25% (5.0 GiB of ~20.0 GiB)") || !strings.Contains(lines[2], "retention") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestProgressPrinterRewritesOneLineOnATerminalAndClearsIt(t *testing.T) {
	var out bytes.Buffer
	printer := newProgressPrinter(&out, true, time.Now)
	printer.show(backup.Progress{Job: "shop", Repository: "local", Percent: -1, BytesDone: 1 << 20})
	printer.show(backup.Progress{Job: "shop", Repository: "local", Percent: -1, BytesDone: 2 << 20})
	printer.done()
	got := out.String()
	if strings.Contains(strings.TrimSuffix(got, "\r\033[K"), "\n") || strings.Count(got, "\r\033[K") != 3 || !strings.Contains(got, "2.0 MiB so far") {
		t.Errorf("output = %q", got)
	}
}
