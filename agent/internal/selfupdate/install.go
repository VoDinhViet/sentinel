package selfupdate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Install puts the staged binary in place of the one at exe, keeping that one as exe.prev. It runs outside the
// sandbox of the daemon (in a transient unit), as root, and only does what the note says: the staged file has
// to be the very file the note names by its SHA-256, so what was checked in the daemon is what is installed.
func Install(dir, exe string) error {
	marker, found, err := ReadMarker(dir)
	if err != nil {
		return err
	}
	if !found || marker.State != Staged {
		return errors.New("there is no staged update to install")
	}
	if time.Since(marker.At) > staleStaged {
		return errors.New("the staged update is old: stage it again")
	}
	staged := filepath.Join(dir, StagedName)
	if err := regularFile(staged); err != nil {
		return err
	}
	if sum, err := sumFile(staged); err != nil || sum != marker.SHA256 {
		return fmt.Errorf("the staged binary is not the one that was checked (sha256 %v)", sum)
	}
	if err := regularFile(exe); err != nil {
		return err
	}
	next := exe + ".new"
	if err := copyFile(staged, next, 0o755); err != nil {
		return fmt.Errorf("copy the new binary next to %s: %w", exe, err)
	}
	prev := exe + ".prev"
	_ = os.Remove(prev)
	if err := os.Link(exe, prev); err != nil { // the old inode stays under its new name: nothing is copied
		if err := copyFile(exe, prev, 0o755); err != nil {
			_ = os.Remove(next)
			return fmt.Errorf("keep the running binary as %s: %w", prev, err)
		}
	}
	if err := os.Rename(next, exe); err != nil {
		_ = os.Remove(next)
		return fmt.Errorf("put the new binary in place: %w", err)
	}
	marker.State, marker.Starts, marker.At = Installed, 0, time.Now()
	if err := WriteMarker(dir, marker); err != nil {
		return fmt.Errorf("keep the note of the update: %w", err)
	}
	_ = os.Remove(staged)
	return nil
}

// Rollback puts exe.prev back as exe, and notes why.
func Rollback(dir, exe, reason string) error {
	prev := exe + ".prev"
	if err := regularFile(prev); err != nil {
		return fmt.Errorf("there is no previous binary to go back to: %w", err)
	}
	marker, found, _ := ReadMarker(dir)
	if !found || marker.State != Installed {
		return errors.New("there is no update on trial to go back from")
	}
	if err := os.Rename(prev, exe); err != nil {
		return fmt.Errorf("put the previous binary back: %w", err)
	}
	marker.State, marker.Reason, marker.At = RolledBack, reason, time.Now()
	if err := WriteMarker(dir, marker); err != nil {
		return fmt.Errorf("keep the note of the update: %w", err)
	}
	return nil
}

// regularFile says path is a plain file, not a link, a directory or a device.
func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

// copyFile copies src to dst, which must not exist, and makes sure it is on disk.
func copyFile(src, dst string, mode os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return errors.Join(out.Close(), os.Chmod(dst, mode))
}
