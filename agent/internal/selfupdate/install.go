package selfupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"sentinel/pkg/release"
)

// Install puts the staged binary in place of the one at exe, keeping that one as exe.prev. It runs outside the
// sandbox of the daemon (in a transient unit), as root, and trusts nothing the daemon wrote: the signed manifest
// kept next to the note is checked again here, with the public key from the config this process read, and the bytes
// that are put in place are hashed while they are copied, so what is installed is what was signed.
func Install(dir, exe string, key ed25519.PublicKey) error {
	manifest, marker, err := Check(dir, key)
	if err != nil {
		return err
	}
	staged := filepath.Join(dir, StagedName)
	if err := regularFile(staged); err != nil {
		return err
	}
	if err := regularFile(exe); err != nil {
		return err
	}
	next := exe + ".new"
	_ = os.Remove(next) // what an install that was killed left; the directory of the binary is root's
	sum, err := copyFile(staged, next, 0o755)
	if err != nil {
		return fmt.Errorf("copy the new binary next to %s: %w", exe, err)
	}
	if sum != manifest.SHA256 {
		_ = os.Remove(next)
		return fmt.Errorf("the staged binary is not the one that was signed (sha256 %s…)", sum[:12])
	}
	prev := exe + ".prev"
	_ = os.Remove(prev)
	if err := os.Link(exe, prev); err != nil { // the old inode stays under its new name: nothing is copied
		if _, err := copyFile(exe, prev, 0o755); err != nil {
			_ = os.Remove(next)
			return fmt.Errorf("keep the running binary as %s: %w", prev, err)
		}
	}
	// The note says "installed" before the binary is in place: a kill between the two leaves a trial that rolls back,
	// not a new binary nobody is watching.
	staged1 := marker
	marker.State, marker.Starts, marker.At = Installed, 0, time.Now()
	if err := WriteMarker(dir, marker); err != nil {
		_ = os.Remove(next)
		return fmt.Errorf("keep the note of the update: %w", err)
	}
	if err := os.Rename(next, exe); err != nil {
		_ = os.Remove(next)
		_ = WriteMarker(dir, staged1)
		return fmt.Errorf("put the new binary in place: %w", err)
	}
	syncDir(filepath.Dir(exe))
	_ = os.Remove(staged)
	return nil
}

// Check is what an install needs to be true before anything is changed: the directory of the update is private, the note
// says an update is staged and fresh (and not that one is on trial), and the signed manifest kept with it is good for the
// key and says what the note says. The helper takes the copy of the state database only after this passes.
func Check(dir string, key ed25519.PublicKey) (release.Manifest, Marker, error) {
	if err := privateDir(dir); err != nil {
		return release.Manifest{}, Marker{}, err
	}
	marker, found, err := ReadMarker(dir)
	if err != nil {
		return release.Manifest{}, Marker{}, err
	}
	if !found || marker.State != Staged {
		return release.Manifest{}, Marker{}, errors.New("there is no staged update to install (or one is on trial: wait for it to report)")
	}
	if time.Since(marker.At) > staleStaged {
		return release.Manifest{}, Marker{}, errors.New("the staged update is old: stage it again")
	}
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return release.Manifest{}, Marker{}, fmt.Errorf("read the signed manifest: %w", err)
	}
	manifest, err := release.Verify(key, raw)
	if err != nil {
		return release.Manifest{}, Marker{}, fmt.Errorf("the signed manifest kept with the update: %w", err)
	}
	if manifest.SHA256 != marker.SHA256 || manifest.Version != marker.To {
		return release.Manifest{}, Marker{}, errors.New("the note of the update does not say what the signed manifest says")
	}
	return manifest, marker, nil
}

// CanRollback says a rollback would work: an update is on trial and the previous binary is there. The helper checks it
// before it touches the state database, so a rollback that cannot happen does not change anything.
func CanRollback(dir, exe string) error {
	if err := regularFile(exe + ".prev"); err != nil {
		return fmt.Errorf("there is no previous binary to go back to: %w", err)
	}
	if !OnTrialNow(dir) {
		return errors.New("there is no update on trial to go back from")
	}
	return nil
}

// privateDir says the directory of the update can be written by nobody but its owner.
func privateDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s can be written by others (%v)", dir, info.Mode().Perm())
	}
	return nil
}

// syncDir makes a rename in dir survive a power cut, as far as it can be asked.
func syncDir(dir string) {
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
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

// copyFile copies src to dst, which must not exist, makes sure it is on disk and returns the SHA-256 of what it wrote.
func copyFile(src, dst string, mode os.FileMode) (sum string, err error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	hash := sha256.New()
	if _, err = io.Copy(io.MultiWriter(out, hash), in); err != nil {
		_ = out.Close()
		return "", err
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		return "", err
	}
	if err = errors.Join(out.Close(), os.Chmod(dst, mode)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
