package main

import (
	"os"
	"path/filepath"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// gitMarker marks a repository's top: a directory, or a file in a worktree
// or submodule.
const gitMarker = ".git"

// findRoot walks up from the directory start and returns the first
// directory holding one of markers or a .git, as exists reports. So the
// nearest marker wins, and a repository's top bounds the walk: a marker
// above it is never used. With neither found, it returns start. The walk
// is by name, not resolving symbolic links, so the root is spelt as start
// is.
func findRoot(start string, markers []string, exists func(path string) bool) string {
	for d := start; ; {
		for _, m := range markers {
			if exists(filepath.Join(d, m)) {
				return d
			}
		}
		if exists(filepath.Join(d, gitMarker)) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return start
		}
		d = parent
	}
}

// detectRoot returns the project root of the absolute file path: the
// nearest directory above it marking a project in its language, or a
// repository, or else the file's own directory. It always contains path.
func detectRoot(path string) string {
	return findRoot(filepath.Dir(path), lsp.RootMarkers(path), exists)
}

// exists reports whether path names something, following symbolic links.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
