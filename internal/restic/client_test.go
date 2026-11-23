package restic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeRestic writes a script that prints out and exits with code.
func fakeRestic(t *testing.T, out string, code int) (bin string) {
	t.Helper()
	bin = filepath.Join(t.TempDir(), "restic")
	script := "#!/bin/sh\n" +
		"cat <<'EOF'\n" + out + "\nEOF\n" +
		"exit " + strconv.Itoa(code) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestIncompleteSnapshotIsFailure(t *testing.T) {
	bin := fakeRestic(t, `{"message_type":"error","error":{"message":"open /x: permission denied"},"item":"/x"}
{"message_type":"summary","files_new":3,"snapshot_id":"abc123"}`, 3)
	client := &Client{Bin: bin, Host: "h"}
	sum, err := client.Backup(context.Background(), BackupOptions{Paths: []string{"/x"}})
	if !errors.Is(err, ErrIncomplete) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
	if sum.SnapshotID != "abc123" {
		t.Errorf("summary should still be parsed: %+v", sum)
	}
}

func TestLockedRepository(t *testing.T) {
	bin := fakeRestic(t, `{"message_type":"exit_error","code":11,"message":"Fatal: unable to create lock"}`, 11)
	_, err := (&Client{Bin: bin, Host: "h"}).Snapshots(context.Background(), nil)
	if !IsLocked(err) {
		t.Fatalf("IsLocked(%v) = false", err)
	}
}

func TestWrongPassword(t *testing.T) {
	bin := fakeRestic(t, `{"message_type":"exit_error","code":12,"message":"Fatal: wrong password or no key found"}`, 12)
	_, err := (&Client{Bin: bin, Host: "h"}).Snapshots(context.Background(), nil)
	if !IsWrongPass(err) || IsLocked(err) {
		t.Fatalf("IsWrongPass(%v) = false", err)
	}
}

func TestVersion(t *testing.T) {
	bin := fakeRestic(t, "restic 0.17.3 compiled with go1.23.1 on linux/amd64", 0)
	ver, err := Version(context.Background(), bin)
	if err != nil || ver != [3]int{0, 17, 3} {
		t.Fatalf("Version = %v, %v", ver, err)
	}
}
