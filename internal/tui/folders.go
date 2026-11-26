package tui

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// suggestionRoots are folders worth backing up on most servers; the ones that exist and can be
// read are suggested. A variable so that tests can point it at a temporary tree.
var suggestionRoots = []string{"/etc", "/var/www", "/srv", "/opt", "/usr/local/etc", "/root"}

// homeRoot holds one folder per user; each of them is suggested too.
var homeRoot = "/home"

// maxSuggestions keeps the line under the Paths field short.
const maxSuggestions = 8

// readableDir reports whether path is a folder this process can list.
func readableDir(path string) bool {
	fileInfo, err := os.Stat(path)
	if err != nil || !fileInfo.IsDir() {
		return false
	}
	dir, err := os.Open(path)
	if err != nil {
		return false
	}
	dir.Close()
	return true
}

// suggestFolders lists the common folders that exist on this machine and are not in have yet
// (the paths already in the field; ${VAR} is expanded before comparing).
func suggestFolders(have []string) []string {
	taken := map[string]bool{}
	for _, path := range have {
		taken[filepath.Clean(os.ExpandEnv(path))] = true
	}
	candidates := slices.Clone(suggestionRoots)
	if users, err := os.ReadDir(homeRoot); err == nil {
		for _, user := range users {
			if user.IsDir() && !strings.HasPrefix(user.Name(), ".") && user.Name() != "lost+found" {
				candidates = append(candidates, filepath.Join(homeRoot, user.Name()))
			}
		}
	}
	var suggestions []string
	for _, folder := range candidates {
		if !taken[filepath.Clean(folder)] && readableDir(folder) {
			suggestions = append(suggestions, folder)
		}
		if len(suggestions) == maxSuggestions {
			break
		}
	}
	return suggestions
}

// addPath appends folder to a comma separated list of paths, once.
func addPath(pathsText, folder string) string {
	paths := splitList(pathsText)
	if !slices.Contains(paths, folder) {
		paths = append(paths, folder)
	}
	return strings.Join(paths, ", ")
}

// Limits that keep the folder browser from stalling on a huge tree.
const (
	maxSizeFiles   = 20000                  // files counted for the size estimate of one folder
	maxSizeTime    = 300 * time.Millisecond // time spent on that estimate
	maxDeepDepth   = 4                      // levels below the current folder the search looks into
	maxDeepVisited = 5000                   // folders the search lists
	maxDeepResults = 15
	maxDeepTime    = 150 * time.Millisecond
)

// deepSkip are trees that hold no data worth finding by name (or are endless).
var deepSkip = []string{"/proc", "/sys", "/dev", "/run", "/var/lib/docker", "/snap"}

func skippedTree(path string) bool {
	return slices.Contains(deepSkip, path)
}

// dirSize adds up the sizes of the files under root, without following symlinks. It stops after
// maxFiles files or maxTime and then reports complete=false: the figure is a lower bound.
func dirSize(root string, maxFiles int, maxTime time.Duration) (bytes int64, complete bool) {
	deadline := time.Now().Add(maxTime)
	files := 0
	err := filepath.WalkDir(root, func(path string, dirEntry fs.DirEntry, err error) error {
		switch {
		case err != nil || dirEntry.Type()&fs.ModeSymlink != 0:
			return nil // unreadable or a link: not counted
		case dirEntry.IsDir():
			if skippedTree(path) {
				return filepath.SkipDir
			}
			return nil
		}
		files++
		if files > maxFiles || time.Now().After(deadline) {
			return fs.SkipAll
		}
		if fileInfo, err := dirEntry.Info(); err == nil {
			bytes += fileInfo.Size()
		}
		return nil
	})
	return bytes, err == nil && files <= maxFiles && !time.Now().After(deadline)
}

// deepSearch looks below root for folders whose name contains needle, two levels or more down
// (the first level is the folder list itself). Hidden folders, symlinks and skippedTree are left out.
func deepSearch(root, needle string) []string {
	needle = strings.ToLower(needle)
	deadline := time.Now().Add(maxDeepTime)
	type level struct {
		path  string
		depth int
	}
	queue := []level{{root, 0}}
	var found []string
	visited := 0
	for len(queue) > 0 && len(found) < maxDeepResults && visited < maxDeepVisited && time.Now().Before(deadline) {
		current := queue[0]
		queue = queue[1:]
		children, err := os.ReadDir(current.path)
		if err != nil {
			continue
		}
		for _, child := range children {
			path := filepath.Join(current.path, child.Name())
			if !child.IsDir() || strings.HasPrefix(child.Name(), ".") || skippedTree(path) {
				continue
			}
			visited++
			if current.depth >= 1 && strings.Contains(strings.ToLower(child.Name()), needle) && len(found) < maxDeepResults {
				found = append(found, path)
			}
			if current.depth+1 < maxDeepDepth {
				queue = append(queue, level{path, current.depth + 1})
			}
		}
	}
	return found
}
