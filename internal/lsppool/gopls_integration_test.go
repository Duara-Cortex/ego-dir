//go:build integration

// What a warm gopls needs to see an edit to a file that is not open. Run
// with `make integration`, and -v to read what gopls does unprompted. It is
// skipped when gopls is not on PATH.
package lsppool_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
	"github.com/Duara-Cortex/ego-dir/internal/lsppool"
)

// goplsSample copies the sample to a directory with no symbolic links in
// its path, as gopls reports paths with links resolved.
func goplsSample(t *testing.T) (gopls, root, shapes, report string) {
	t.Helper()
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not on PATH")
	}
	root, _, _ = sample(t)
	if root, err = filepath.EvalSymlinks(root); err != nil {
		t.Fatal(err)
	}
	return gopls, root, lsp.FileURI(filepath.Join(root, lspfake.SampleFile)), lsp.FileURI(filepath.Join(root, lspfake.SampleReportFile))
}

type caller interface {
	PrepareCallHierarchy(ctx context.Context, uri string, pos lsp.Position) ([]lsp.CallHierarchyItem, error)
	IncomingCalls(ctx context.Context, item lsp.CallHierarchyItem) ([]lsp.CallHierarchyIncomingCall, error)
}

func goplsCallers(t *testing.T, c caller, shapes string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	items, err := c.PrepareCallHierarchy(ctx, shapes, totalAreaPos)
	if err != nil || len(items) != 1 {
		t.Fatalf("PrepareCallHierarchy = %v, %v", items, err)
	}
	calls, err := c.IncomingCalls(ctx, items[0])
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, call := range calls {
		names = append(names, call.From.Name)
	}
	sort.Strings(names)
	return names
}

// warmGopls starts gopls on root, asks for TotalArea's callers with
// shapes.go open, closes it, and adds a caller in report.go on disk.
func warmGopls(t *testing.T, gopls, root, shapes string) *lsp.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := lsp.StartWith(ctx, []string{gopls}, root, lsp.Config{WatchFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	text, err := os.ReadFile(filepath.Join(root, lspfake.SampleFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.DidOpen(ctx, shapes, "go", string(text)); err != nil {
		t.Fatal(err)
	}
	if got := goplsCallers(t, c, shapes); !reflect.DeepEqual(got, []string{"Report"}) {
		t.Fatalf("callers before the edit = %v, want [Report]", got)
	}
	if err := c.DidClose(ctx, shapes); err != nil {
		t.Fatal(err)
	}
	appendSummary(t, root)
	return c
}

func TestGoplsRegistersForWatchedFiles(t *testing.T) {
	gopls, root, shapes, _ := goplsSample(t)
	c := warmGopls(t, gopls, root, shapes)
	deadline := time.Now().Add(10 * time.Second)
	for !c.WatchesFiles() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !c.WatchesFiles() {
		t.Error("gopls did not register for workspace/didChangeWatchedFiles; the pool would restart it on every edit")
	}
}

func TestGoplsEditWithoutNotification(t *testing.T) {
	gopls, root, shapes, _ := goplsSample(t)
	c := warmGopls(t, gopls, root, shapes)
	got := goplsCallers(t, c, shapes)
	if reflect.DeepEqual(got, []string{"Report"}) {
		t.Logf("gopls serves the stale callers %v without a notification: it must be told", got)
	} else {
		t.Logf("gopls saw the edit by itself (callers %v): the notification is a safety net", got)
	}
}

func TestGoplsEditWithNotification(t *testing.T) {
	gopls, root, shapes, report := goplsSample(t)
	c := warmGopls(t, gopls, root, shapes)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.DidChangeWatchedFiles(ctx, []lsp.FileEvent{{URI: report, Type: lsp.FileChanged}}); err != nil {
		t.Fatal(err)
	}
	if got := goplsCallers(t, c, shapes); !reflect.DeepEqual(got, []string{"Report", "Summary"}) {
		t.Errorf("callers after didChangeWatchedFiles = %v, want [Report Summary]", got)
	}
}

func TestGoplsEditWithRestart(t *testing.T) {
	gopls, root, shapes, _ := goplsSample(t)
	warmGopls(t, gopls, root, shapes).Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := lsp.Start(ctx, []string{gopls}, root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	if got := goplsCallers(t, c, shapes); !reflect.DeepEqual(got, []string{"Report", "Summary"}) {
		t.Errorf("callers from a new gopls = %v, want [Report Summary]", got)
	}
}

func TestGoplsPoolSeesEdits(t *testing.T) {
	gopls, root, shapes, _ := goplsSample(t)
	poolSeesEdits(t, gopls, root, shapes)
}

// A --root reached through a symbolic link: the pool scans and names files
// by the link, which gopls must match to its own paths.
func TestGoplsPoolSeesEditsThroughASymlinkedRoot(t *testing.T) {
	gopls, real, _, _ := goplsSample(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("cannot make a symbolic link:", err)
	}
	poolSeesEdits(t, gopls, link, lsp.FileURI(filepath.Join(link, lspfake.SampleFile)))
}

func poolSeesEdits(t *testing.T, gopls, root, shapes string) {
	t.Helper()
	var log syncBuffer
	p := lsppool.New(lsppool.Options{Idle: time.Minute, Log: &log})
	defer p.Close(context.Background())
	s, err := p.Acquire(context.Background(), []string{gopls}, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := goplsCallers(t, s, shapes); !reflect.DeepEqual(got, []string{"Report"}) {
		t.Fatalf("callers = %v", got)
	}
	s.Release(context.Background(), nil)
	appendSummary(t, root)
	if s, err = p.Acquire(context.Background(), []string{gopls}, root); err != nil {
		t.Fatal(err)
	}
	defer s.Release(context.Background(), nil)
	if got := goplsCallers(t, s, shapes); !reflect.DeepEqual(got, []string{"Report", "Summary"}) {
		t.Errorf("callers after the edit = %v, want [Report Summary]", got)
	}
	if log.String() != "" {
		t.Errorf("pool log = %q", log.String())
	}
}
