package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// PrepareRestoreTarget makes sure targetDir is a folder restic can restore into without being steered
// elsewhere. restic runs as the user of the daemon (usually root) and writes through whatever it finds at the
// target, so a folder that another user made first, and filled with symlinks such as etc -> /etc,
// would let that user overwrite files they cannot touch. A missing folder is created with mode 0700;
// an existing one must be a real folder (not a symlink) that the current user owns and nobody else
// can write to. trustOwner skips the last two checks, for a target the operator knows is theirs but
// that cannot satisfy them (an NFS, CIFS or vfat mount reports other owners and modes, a folder of
// another user while running under sudo); a symlink is still refused.
func PrepareRestoreTarget(targetDir string, trustOwner bool) error {
	if targetDir == "" || !filepath.IsAbs(targetDir) || strings.ContainsRune(targetDir, 0) {
		return fmt.Errorf("restore target %q must be an absolute path without NUL bytes", targetDir)
	}
	targetDir = filepath.Clean(targetDir)
	// Parents get the usual mode so the user who owns the tree, or the database user who loads a dump,
	// can still reach the target; only the folder restic writes into is private.
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return fmt.Errorf("create restore target: %w", err)
	}
	// Mkdir fails when the path exists, which makes creating it atomic: a folder somebody else made
	// first is inspected below instead of being used.
	if err := os.Mkdir(targetDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create restore target: %w", err)
	}
	info, err := os.Lstat(targetDir)
	if err != nil {
		return fmt.Errorf("check restore target: %w", err)
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("restore target %s is a symbolic link: use the folder it points to", targetDir)
	case !info.IsDir():
		return fmt.Errorf("restore target %s is not a folder", targetDir)
	}
	if trustOwner {
		return nil
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("restore target %s is owned by user %d, not by the user running sentinel", targetDir, stat.Uid)
	}
	if mode := info.Mode().Perm(); mode&0o022 != 0 {
		return fmt.Errorf("restore target %s: other users can write to it (mode %04o); run chmod 700 on it", targetDir, mode)
	}
	return nil
}

// systemRestoreTargets are folders a restore would write into the running system or a database's data.
var systemRestoreTargets = map[string]bool{
	"/": true, "/etc": true, "/usr": true, "/bin": true, "/sbin": true, "/lib": true, "/lib64": true,
	"/boot": true, "/var": true, "/root": true, "/home": true, "/dev": true, "/proc": true, "/sys": true,
	"/run":                true,
	"/var/lib/postgresql": true, "/var/lib/mysql": true, "/var/lib/mongodb": true,
}

// IsSystemRestoreTarget reports whether dir is the root of the system or one of its core folders. A
// snapshot of files is restored with its original paths under the target, so restoring into "/"
// overwrites the live system; the CLI asks for --force before doing that.
func IsSystemRestoreTarget(dir string) bool {
	return systemRestoreTargets[filepath.Clean(dir)]
}
