package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxOptions is how many completions are listed under a field; the rest are counted.
const maxOptions = 12

// completePath completes the path token like a shell does on Tab. It keeps what was typed
// (${HOME}/ and ~/ stay as they are) and completes the last name: one match is filled in
// (with a / after a folder), several are filled in as far as they agree and listed in options.
// A token that is not an absolute path, ~ or ${VAR} path, or that matches nothing, is returned
// unchanged.
func completePath(token string) (completed string, options []string) {
	if token == "" || !strings.ContainsAny(token[:1], "/~$") {
		return token, nil
	}
	slash := strings.LastIndex(token, "/")
	if slash < 0 { // "~" or "${HOME}" alone: step into the folder
		if readableDir(expandPath(token)) {
			return token + "/", nil
		}
		return token, nil
	}
	dirText, base := token[:slash+1], token[slash+1:]
	entries, err := os.ReadDir(expandPath(dirText))
	if err != nil {
		return token, nil
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, base) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		if isFolder(filepath.Join(expandPath(dirText), name), entry) {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	switch len(names) {
	case 0:
		return token, nil
	case 1:
		return dirText + names[0], nil
	}
	common := names[0]
	for _, name := range names[1:] {
		for !strings.HasPrefix(name, common) {
			common = common[:len(common)-1]
		}
	}
	if len(names) > maxOptions {
		names = append(names[:maxOptions], fmt.Sprintf("+%d nữa", len(names)-maxOptions))
	}
	return dirText + common, names
}

// completeList completes the last item of a comma separated list of paths (or the whole value
// of a field that holds one path), keeping what comes before it.
func completeList(value string, list bool) (completed string, options []string) {
	head, tail := "", value
	if list {
		if comma := strings.LastIndex(value, ","); comma >= 0 {
			head, tail = value[:comma+1], value[comma+1:]
		}
	}
	token := strings.TrimLeft(tail, " ")
	lead := tail[:len(tail)-len(token)]
	completed, options = completePath(token)
	return head + lead + completed, options
}

// expandPath turns ~ and ${VAR} into the path they stand for.
func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = home + path[1:]
		}
	}
	return os.ExpandEnv(path)
}

// isFolder tells a folder from a file, following a symlink.
func isFolder(path string, entry os.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if entry.Type()&os.ModeSymlink != 0 {
		info, err := os.Stat(path)
		return err == nil && info.IsDir()
	}
	return false
}
