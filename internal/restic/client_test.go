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

func TestBackupReportsProgressAndKeepsTheRest(t *testing.T) {
	bin := fakeRestic(t, `{"message_type":"status","seconds_elapsed":1,"percent_done":0,"total_files":1,"bytes_done":1000}
{"message_type":"status","seconds_elapsed":3,"percent_done":0.5,"total_files":4,"files_done":2,"total_bytes":4000,"bytes_done":2000}
{"message_type":"error","error":{"message":"open /x: permission denied"},"item":"/x"}
{"message_type":"summary","files_new":3,"snapshot_id":"abc123","total_bytes_processed":4000}`, 3)
	client := &Client{Bin: bin, Host: "h"}
	var seen []Status
	sum, err := client.Backup(context.Background(), BackupOptions{Paths: []string{"/x"}, OnStatus: func(status Status) { seen = append(seen, status) }})
	if len(seen) != 2 || seen[0].BytesDone != 1000 || seen[0].TotalBytes != 0 || seen[1].BytesDone != 2000 || seen[1].TotalBytes != 4000 ||
		seen[1].PercentDone != 0.5 || seen[1].FilesDone != 2 || seen[1].Elapsed.Seconds() != 3 {
		t.Errorf("progress = %+v", seen)
	}
	if !errors.Is(err, ErrIncomplete) || !strings.Contains(err.Error(), "permission denied") || sum.SnapshotID != "abc123" {
		t.Errorf("the error and the summary are still read: %v, %+v", err, sum)
	}
}

func TestBackupWithoutAProgressHandlerKeepsEveryLine(t *testing.T) {
	bin := fakeRestic(t, `{"message_type":"status","bytes_done":1000}
{"message_type":"summary","files_new":1,"snapshot_id":"abc123"}`, 0)
	if sum, err := (&Client{Bin: bin, Host: "h"}).Backup(context.Background(), BackupOptions{Paths: []string{"/x"}}); err != nil || sum.SnapshotID != "abc123" {
		t.Errorf("%+v, %v", sum, err)
	}
}

func TestStatusFilterHandlesLinesSplitAcrossWrites(t *testing.T) {
	var seen []Status
	filter := &statusFilter{onStatus: func(status Status) { seen = append(seen, status) }}
	for _, piece := range []string{`{"message_type":"sta`, `tus","bytes_done":7}` + "\n" + `{"message_type":"summary","snap`, `shot_id":"x"}`} {
		if _, err := filter.Write([]byte(piece)); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 1 || seen[0].BytesDone != 7 {
		t.Errorf("status = %+v", seen)
	}
	if got := string(filter.Bytes()); got != `{"message_type":"summary","snapshot_id":"x"}` {
		t.Errorf("kept = %q (a last line without a newline is kept too)", got)
	}
}

func TestStatusLinesAreNotKeptSoALongBackupDoesNotGrowMemory(t *testing.T) {
	filter := &statusFilter{onStatus: func(Status) {}}
	for range 10000 {
		filter.Write([]byte(`{"message_type":"status","bytes_done":1}` + "\n"))
	}
	if len(filter.Bytes()) != 0 {
		t.Errorf("%d bytes of status lines were kept", len(filter.Bytes()))
	}
}
