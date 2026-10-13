//go:build integration

// Integration test against a real gopls. Run with `make integration`.
// It is skipped when gopls is not on PATH.
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

// goplsSource starts gopls on the sample workspace.
func goplsSource(t *testing.T) (*lspsource.Source, string) {
	t.Helper()
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not on PATH")
	}
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := lsp.Start(ctx, []string{gopls}, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return lspsource.New(c, root), root
}

// TestGoplsMatchesFake builds the same graphs from gopls and from the fake,
// for every symbol in the sample, and requires them to be identical.
func TestGoplsMatchesFake(t *testing.T) {
	src, root := goplsSource(t)
	all := []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports,
		graph.EdgeContains, graph.EdgeReferences}
	shapes, report := lspfake.SampleFile, lspfake.SampleReportFile
	egos := []struct {
		name      string
		file      string
		line, col int // 1-based
	}{
		{"Shape", shapes, 4, 6}, {"Shape.Area", shapes, 5, 2}, {"Rect", shapes, 9, 6}, {"Rect.W", shapes, 9, 19},
		{"Rect.H", shapes, 9, 22}, {"Rect.Area", shapes, 12, 15}, {"NewRect", shapes, 15, 6},
		{"TotalArea", shapes, 18, 6}, {"Report", shapes, 27, 6}, {"TotalArea call site", shapes, 28, 9},
		{"package sample", shapes, 1, 9}, {"package report", report, 2, 9},
		{"Describe", report, 12, 6}, {"Label", report, 17, 6}, {"Rect use in report", report, 12, 24},
	}
	for _, ego := range egos {
		for _, depth := range []int{1, 2} {
			for _, d := range []struct {
				name string
				dir  graph.Direction
			}{{"in", graph.DirIn}, {"out", graph.DirOut}, {"both", graph.DirBoth}} {
				opts := graph.Options{Depth: depth, Kinds: all, Direction: d.dir, Body: graph.BodyAll}
				t.Run(fmt.Sprintf("%s/depth%d/%s", ego.name, depth, d.name), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					uri := lsp.FileURI(filepath.Join(root, filepath.FromSlash(ego.file)))
					real, err := graph.Build(ctx, src, uri, ego.line-1, ego.col-1, opts)
					if err != nil {
						t.Fatal(err)
					}
					fake := setup(t).buildIn(t, ego.file, ego.line, ego.col, opts)
					if !reflect.DeepEqual(real.Nodes, fake.Nodes) {
						t.Errorf("nodes differ\ngopls %+v\nfake  %+v", real.Nodes, fake.Nodes)
					}
					if !reflect.DeepEqual(real.Edges, fake.Edges) {
						rn, re := summary(real)
						fn, fe := summary(fake)
						t.Errorf("edges differ\ngopls %v %v\nfake  %v %v", rn, re, fn, fe)
					}
				})
			}
		}
	}
}

// TestGoplsGraphIsClean checks the gopls answers that the fake cannot
// drift on: no node outside the workspace, no hover decoration.
func TestGoplsGraphIsClean(t *testing.T) {
	src, root := goplsSource(t)
	uri := lsp.FileURI(filepath.Join(root, lspfake.SampleFile))
	rootURI := lsp.FileURI(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	kinds := []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports, graph.EdgeContains}
	for _, at := range []struct {
		file      string
		line, col int
	}{
		{lspfake.SampleFile, 18, 6}, {lspfake.SampleFile, 15, 6}, {lspfake.SampleFile, 9, 6}, // TotalArea, NewRect, Rect
		{lspfake.SampleFile, 1, 9}, {lspfake.SampleReportFile, 2, 9}, // the packages
	} {
		fileURI := lsp.FileURI(filepath.Join(root, filepath.FromSlash(at.file)))
		g, err := graph.Build(ctx, src, fileURI, at.line-1, at.col-1, graph.Options{Depth: 2, Kinds: kinds})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range g.Nodes {
			// A module outside the workspace has no URI at all.
			if n.URI != "" && n.URI != rootURI && !strings.HasPrefix(n.URI, rootURI+"/") {
				t.Errorf("%s is outside the workspace: %s", n.Name, n.URI)
			}
			for _, s := range []string{n.Signature, n.Doc} {
				for _, junk := range []string{"---", "size=", "pkg.go.dev", "```", "Package path:", "Language version:"} {
					if strings.Contains(s, junk) {
						t.Errorf("%s: %q contains %q", n.Name, s, junk)
					}
				}
			}
		}
	}
	g, err := graph.Build(ctx, src, uri, 14, 5, graph.Options{Depth: 1, Kinds: kinds, Direction: graph.DirOut})
	if err != nil {
		t.Fatal(err)
	}
	if _, edges := summary(g); !reflect.DeepEqual(edges, []string{"NewRect@15 -instantiates-> Rect@9 x1"}) {
		t.Errorf("NewRect edges = %v, want it to instantiate Rect", edges)
	}
}
