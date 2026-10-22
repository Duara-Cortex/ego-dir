package lsppool

import (
	"bytes"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// racyWindow is how close to a scan a file's modification time must be
// for size and time not to be trusted: a filesystem records times only so
// finely, so an edit of the same size soon after a scan can leave both
// unchanged. Such files are compared by content at the next scan.
const racyWindow = 2 * time.Second

// restartFiles change how a Go server loads the whole workspace.
var restartFiles = map[string]bool{"go.mod": true, "go.sum": true, "go.work": true, "go.work.sum": true}

// snapshot is what a scan saw of the files under a root.
type snapshot struct {
	taken time.Time
	files map[string]fileState // by path
}

type fileState struct {
	size  int64
	mtime time.Time
	hash  []byte // kept while the modification time is too recent to trust
}

// skipDir reports whether a directory below the root is left out of scans:
// hidden directories (.git, editor state) and node_modules.
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules"
}

// scan records the regular files under root and, given an earlier
// snapshot, returns what was created, changed or deleted since, sorted by
// URI. Files that cannot be read are left out.
func scan(root string, prev *snapshot) (*snapshot, []lsp.FileEvent) {
	next := &snapshot{taken: time.Now(), files: map[string]fileState{}}
	var events []lsp.FileEvent
	// A trailing separator makes the walk follow a root that is a symbolic
	// link; paths are still named under root.
	_ = filepath.WalkDir(root+string(filepath.Separator), func(path string, d fs.DirEntry, err error) error {
		path = filepath.Clean(path)
		if err != nil {
			if d != nil && d.IsDir() && path != root {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st := fileState{size: info.Size(), mtime: info.ModTime()}
		var old fileState
		had := false
		if prev != nil {
			old, had = prev.files[path]
		}
		same := had && old.size == st.size && old.mtime.Equal(st.mtime)
		racy := !st.mtime.Before(next.taken.Add(-racyWindow))
		var hash []byte
		if racy || (same && old.hash != nil) {
			hash = hashFile(path)
		}
		if racy {
			st.hash = hash
		}
		next.files[path] = st
		switch {
		case prev == nil:
		case !had:
			events = append(events, lsp.FileEvent{URI: lsp.FileURI(path), Type: lsp.FileCreated})
		case !same || (old.hash != nil && !bytes.Equal(old.hash, hash)):
			events = append(events, lsp.FileEvent{URI: lsp.FileURI(path), Type: lsp.FileChanged})
		}
		return nil
	})
	if prev != nil {
		for path := range prev.files {
			if _, ok := next.files[path]; !ok {
				events = append(events, lsp.FileEvent{URI: lsp.FileURI(path), Type: lsp.FileDeleted})
			}
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].URI < events[j].URI })
	return next, events
}

// hashFile returns the SHA-256 of a file's content, or nil if it cannot be
// read.
func hashFile(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(b)
	return sum[:]
}

// needsRestart reports whether changes are better met by a new server
// than by telling the old one.
func needsRestart(changes []lsp.FileEvent) bool {
	if len(changes) > MaxChanges {
		return true
	}
	for _, c := range changes {
		path, err := lsp.URIToPath(c.URI)
		if err == nil && restartFiles[filepath.Base(path)] {
			return true
		}
	}
	return false
}
