package configedit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// plainEnvValue is what an env file value may hold without quotes, so sh, systemd and docker
// all read it the same way.
var plainEnvValue = regexp.MustCompile(`^[A-Za-z0-9._+/=@-]+$`)

// SaveSecret sets name=value in the env file: it replaces the line of that variable or adds one,
// keeps every other line, and writes the file readable by its owner only (0600) unless it
// already existed, in which case its owner and group read access are kept but nothing for other
// users (see tightenedMode). The file is flushed to disk and replaced in one step, so a crash
// cannot leave half of it.
func SaveSecret(envFile, name, value string) error {
	if !envNameRe.MatchString(name) {
		return fmt.Errorf("%q không phải tên biến môi trường", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("secret không được chứa ký tự xuống dòng")
	}
	if target, err := filepath.EvalSymlinks(envFile); err == nil {
		envFile = target // update what a symlink points to instead of replacing the link
	}
	raw, err := os.ReadFile(envFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(envFile); statErr == nil {
		mode = tightenedMode(info.Mode().Perm())
	}
	line := name + "=" + quoteEnvValue(value)
	var lines []string
	replaced := false
	if len(raw) > 0 {
		for _, existing := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			bare := strings.TrimPrefix(strings.TrimSpace(existing), "export ")
			if strings.HasPrefix(strings.TrimSpace(bare), name+"=") {
				if !replaced {
					lines = append(lines, line)
				}
				replaced = true
				continue
			}
			lines = append(lines, existing)
		}
	}
	if !replaced {
		lines = append(lines, line)
	}
	if err := os.MkdirAll(filepath.Dir(envFile), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(envFile), ".sentinel-env-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if info, statErr := os.Stat(envFile); statErr == nil {
		keepOwner(temp, info) // best effort: the daemon's user keeps reading the file
	}
	if _, err := temp.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), envFile); err != nil {
		return err
	}
	syncDir(filepath.Dir(envFile))
	return nil
}

// tightenedMode drops what the group may write and everything other users may do. Group read stays
// because the daemon may run as a user in the file's group.
func tightenedMode(mode os.FileMode) os.FileMode {
	return mode &^ 0o027
}

// keepOwner gives the new file the owner and group of the file it replaces, when allowed.
func keepOwner(temp *os.File, old os.FileInfo) {
	if stat, ok := old.Sys().(*syscall.Stat_t); ok {
		_ = temp.Chown(int(stat.Uid), int(stat.Gid))
	}
}

func quoteEnvValue(value string) string {
	if plainEnvValue.MatchString(value) {
		return value
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(value)
	return `"` + escaped + `"`
}
