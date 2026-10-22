package lsppool

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// settle backdates a file beyond the racy window.
func settle(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func events(t *testing.T, root string, evs []lsp.FileEvent) []string {
	t.Helper()
	var out []string
	for _, e := range evs {
		path, err := lsp.URIToPath(e.URI)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, map[lsp.FileChangeType]string{lsp.FileCreated: "+", lsp.FileChanged: "~", lsp.FileDeleted: "-"}[e.Type]+filepath.ToSlash(rel))
	}
	return out
}

func TestScanFindsCreatedChangedAndDeletedFiles(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"a.go", "b.go", "sub/c.go"} {
		write(t, filepath.Join(root, f), "package x\n")
		settle(t, filepath.Join(root, f))
	}
	snap, evs := scan(root, nil)
	if evs != nil {
		t.Errorf("a first scan reported %v", evs)
	}
	write(t, filepath.Join(root, "a.go"), "package x // changed\n")
	os.Remove(filepath.Join(root, "b.go"))
	write(t, filepath.Join(root, "sub/d.go"), "package x\n")
	_, evs = scan(root, snap)
	if got, want := events(t, root, evs), []string{"~a.go", "-b.go", "+sub/d.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

func TestScanSkipsHiddenDirectoriesAndNodeModules(t *testing.T) {
	root := t.TempDir()
	snap, _ := scan(root, nil)
	for _, f := range []string{".git/index", ".vscode/settings.json", "node_modules/m/index.js", "web/node_modules/n.js", ".env"} {
		write(t, filepath.Join(root, f), "x")
	}
	_, evs := scan(root, snap)
	if got, want := events(t, root, evs), []string{"+.env"}; !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want only the hidden file at the top (%v)", got, want)
	}
}

func TestScanSeesASameSizeEditWithinTheRacyWindow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	write(t, path, "package aaa\n")
	stamp := time.Now().Truncate(time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	snap, _ := scan(root, nil)
	if snap.files[path].hash == nil {
		t.Fatal("a file modified just now was not hashed")
	}
	// Same size, and the same time a coarse filesystem would record.
	write(t, path, "package bbb\n")
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	snap2, evs := scan(root, snap)
	if got := events(t, root, evs); !reflect.DeepEqual(got, []string{"~a.go"}) {
		t.Errorf("events = %v, want the edit seen by its content", got)
	}
	if _, evs := scan(root, snap2); evs != nil {
		t.Errorf("a rescan with nothing changed reported %v", evs)
	}
}

func TestScanDoesNotHashSettledFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	write(t, path, "package x\n")
	settle(t, path)
	snap, _ := scan(root, nil)
	if snap.files[path].hash != nil {
		t.Error("a file modified an hour ago was hashed")
	}
	if _, evs := scan(root, snap); evs != nil {
		t.Errorf("rescan reported %v", evs)
	}
}

func TestScanOfAMissingRoot(t *testing.T) {
	snap, evs := scan(filepath.Join(t.TempDir(), "gone"), nil)
	if len(snap.files) != 0 || evs != nil {
		t.Errorf("scan of a missing root = %v, %v", snap.files, evs)
	}
}

func TestNeedsRestart(t *testing.T) {
	ev := func(path string) lsp.FileEvent { return lsp.FileEvent{URI: lsp.FileURI(path), Type: lsp.FileChanged} }
	for _, tc := range []struct {
		changes []lsp.FileEvent
		want    bool
	}{
		{[]lsp.FileEvent{ev("/w/a.go")}, false},
		{[]lsp.FileEvent{ev("/w/a.go"), ev("/w/go.mod")}, true},
		{[]lsp.FileEvent{ev("/w/sub/go.sum")}, true},
		{[]lsp.FileEvent{ev("/w/go.work")}, true},
		{[]lsp.FileEvent{ev("/w/go.work.sum")}, true},
		{[]lsp.FileEvent{ev("/w/go.mod.txt")}, false},
	} {
		if got := needsRestart(tc.changes); got != tc.want {
			t.Errorf("needsRestart(%v) = %v, want %v", tc.changes, got, tc.want)
		}
	}
	many := make([]lsp.FileEvent, MaxChanges+1)
	for i := range many {
		many[i] = ev("/w/f" + strings.Repeat("x", i%7) + ".go")
	}
	if !needsRestart(many) {
		t.Errorf("%d changes did not ask for a restart", len(many))
	}
	if needsRestart(many[:MaxChanges]) {
		t.Errorf("%d changes asked for a restart", MaxChanges)
	}
}

// BenchmarkScan times the scan before each call, over this repository.
func BenchmarkScan(b *testing.B) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		b.Fatal(err)
	}
	snap, _ := scan(root, nil)
	for b.Loop() {
		snap, _ = scan(root, snap)
	}
}

func TestScanFollowsASymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	write(t, filepath.Join(real, "a.go"), "package x\n")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("cannot make a symbolic link:", err)
	}
	snap, _ := scan(link, nil)
	if _, ok := snap.files[filepath.Join(link, "a.go")]; !ok || len(snap.files) != 1 {
		t.Fatalf("files = %v, want a.go named under the link", snap.files)
	}
	write(t, filepath.Join(real, "b.go"), "package x\n")
	if _, evs := scan(link, snap); !reflect.DeepEqual(events(t, link, evs), []string{"+b.go"}) {
		t.Errorf("events = %v, want +b.go", events(t, link, evs))
	}
}
