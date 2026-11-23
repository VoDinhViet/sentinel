package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
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
func addPath(list, folder string) string {
	paths := splitList(list)
	if !slices.Contains(paths, folder) {
		paths = append(paths, folder)
	}
	return strings.Join(paths, ", ")
}
