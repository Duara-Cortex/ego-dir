//go:build integration

// Integration tests against a real pyright. Run with `make integration`.
// They are skipped when pyright-langserver is not on PATH.
package lspsource_test

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
	"github.com/Duara-Cortex/ego-dir/internal/lspsource"
)

// pyrightSource starts pyright on the Python workspace. Each caller gets a
// server of its own, as the CLI does: what pyright has open depends on
// what was asked before.
func pyrightSource(t *testing.T) (*lspsource.Source, string) {
	t.Helper()
	server, err := exec.LookPath("pyright-langserver")
	if err != nil {
		t.Skip("pyright-langserver not on PATH")
	}
	root, err := lspfake.PythonRoot()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := lsp.Start(ctx, []string{server, "--stdio"}, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return lspsource.New(c, root), root
}

// TestPyrightMatchesFake builds the same graphs from pyright and from the
// fake, for every symbol in the Python workspace, and requires them to be
// identical.
func TestPyrightMatchesFake(t *testing.T) {
	all := []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports,
		graph.EdgeContains, graph.EdgeReferences}
	shapes, report, pkg := lspfake.PythonFile, lspfake.PythonReportFile, lspfake.PythonPackageFile
	egos := []struct {
		name      string
		file      string
		line, col int // 1-based
	}{
		{"module shapes", shapes, 1, 1}, {"Shape", shapes, 6, 7}, {"Shape.area", shapes, 10, 9}, {"Rect", shapes, 14, 7},
		{"Rect.__init__", shapes, 17, 9}, {"Rect.w", shapes, 18, 14}, {"Rect.h", shapes, 19, 14}, {"Rect.area", shapes, 21, 9},
		{"new_rect", shapes, 26, 5}, {"Rect() call", shapes, 28, 12}, {"total_area", shapes, 31, 5}, {"s.area() call", shapes, 35, 20},
		{"report", shapes, 39, 5}, {"total_area call site", shapes, 41, 12}, {"new_rect call site", shapes, 41, 24},
		{"module report.report", report, 1, 1}, {"Rect import", report, 5, 20}, {"describe", report, 8, 5},
		{"Rect annotation", report, 8, 17}, {"r.w use", report, 10, 25}, {"label", report, 13, 5},
		{"package report", pkg, 1, 1},
	}
	for _, ego := range egos {
		t.Run(ego.name, func(t *testing.T) {
			src, root := pyrightSource(t)
			fake := setupPython(t)
			uri := lsp.FileURI(filepath.Join(root, filepath.FromSlash(ego.file)))
			for _, depth := range []int{1, 2} {
				for _, d := range []struct {
					name string
					dir  graph.Direction
				}{{"in", graph.DirIn}, {"out", graph.DirOut}, {"both", graph.DirBoth}} {
					t.Run(fmt.Sprintf("depth%d/%s", depth, d.name), func(t *testing.T) {
						opts := graph.Options{Depth: depth, Kinds: all, Direction: d.dir, Body: graph.BodyAll}
						ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
						defer cancel()
						real, err := graph.Build(ctx, src, uri, ego.line-1, ego.col-1, opts)
						if err != nil {
							t.Fatal(err)
						}
						faked := fake.buildIn(t, ego.file, ego.line, ego.col, opts)
						if !reflect.DeepEqual(real.Nodes, faked.Nodes) {
							t.Errorf("nodes differ\npyright %+v\nfake    %+v", real.Nodes, faked.Nodes)
						}
						if !reflect.DeepEqual(real.Edges, faked.Edges) {
							rn, re := summary(real)
							fn, fe := summary(faked)
							t.Errorf("edges differ\npyright %v %v\nfake    %v %v", rn, re, fn, fe)
						}
					})
				}
			}
		})
	}
}

// TestPyrightGraphIsClean checks what the fake cannot drift on: no node
// outside the workspace (pyright's stubs, the interpreter's library) and
// nothing of pyright's hover layout left in a signature or doc.
func TestPyrightGraphIsClean(t *testing.T) {
	src, root := pyrightSource(t)
	rootURI := lsp.FileURI(root)
	kinds := []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports,
		graph.EdgeContains, graph.EdgeReferences}
	for _, at := range []struct {
		file      string
		line, col int
	}{
		{lspfake.PythonFile, 31, 5}, {lspfake.PythonFile, 26, 5}, {lspfake.PythonFile, 14, 7}, {lspfake.PythonFile, 1, 1},
		{lspfake.PythonReportFile, 13, 5}, {lspfake.PythonReportFile, 1, 1},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		g, err := graph.Build(ctx, src, lsp.FileURI(filepath.Join(root, filepath.FromSlash(at.file))), at.line-1, at.col-1,
			graph.Options{Depth: 2, Kinds: kinds})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range g.Nodes {
			if n.URI != "" && n.URI != rootURI && !strings.HasPrefix(n.URI, rootURI+"/") {
				t.Errorf("%s is outside the workspace: %s", n.Name, n.URI)
			}
			for _, s := range []string{n.Signature, n.Doc} {
				for _, junk := range []string{"```", "---", "\\_", "&nbsp;", "&gt;", "(function) ", "(method) ", "(variable) "} {
					if strings.Contains(s, junk) {
						t.Errorf("%s: %q contains %q", n.Name, s, junk)
					}
				}
			}
		}
	}
}
