package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// clientVersion runs before every dump and every connection probe, so a client that never answers must not
// hold the job (and its lock) forever: the job's own timeout does not cover this call.
func TestClientVersionGivesUpOnAClientThatHangs(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "mariadb-dump")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldTimeout := clientVersionTimeout
	clientVersionTimeout = 200 * time.Millisecond
	t.Cleanup(func() { clientVersionTimeout = oldTimeout })

	start := time.Now()
	if got := clientVersion(bin); got != "" {
		t.Errorf("clientVersion = %q, want nothing from a client that did not answer", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("clientVersion took %s, want it to stop after the timeout", elapsed)
	}
}

func TestClientVersionReadsWhatTheClientPrints(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "mysqldump")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'mysqldump  Ver 8.0.36'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := clientVersion(bin); got != "mysqldump  Ver 8.0.36\n" {
		t.Errorf("clientVersion = %q", got)
	}
}
