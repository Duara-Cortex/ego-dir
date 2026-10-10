//go:build integration

// Integration test against a real gopls. Run with `make integration`.
// It is skipped when gopls is not on PATH.
package lspsource_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rasJacobCharles/ego-dir/internal/graph"
	"github.com/rasJacobCharles/ego-dir/internal/lsp"
	"github.com/rasJacobCharles/ego-dir/internal/lsp/lspfake"
	"github.com/rasJacobCharles/ego-dir/internal/lspsource"
)

func TestGoplsMatchesFake(t *testing.T) {
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
	defer c.Close(context.Background())

	uri := lsp.FileURI(filepath.Join(root, lspfake.SampleFile))
	g, err := graph.Build(ctx, lspsource.New(c, root), uri, 17, 5, graph.Options{
		Depth: 2, Kinds: []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements}, Direction: graph.DirBoth,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes, edges := summary(g)
	// Same shape as the fake (TestCallsDepthTwo), without relying on gopls
	// range details beyond the declaration line.
	wantNodes := []string{"TotalArea@18 hop0", "Area@5 hop1", "Report@27 hop1", "Area@12 hop2"}
	wantEdges := []string{
		"Area@12 -implements-> Area@5 x1",
		"Report@27 -calls-> TotalArea@18 x1",
		"TotalArea@18 -calls-> Area@5 x1",
	}
	if !reflect.DeepEqual(nodes, wantNodes) || !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("gopls graph differs from the fake:\nnodes %v\nedges %v", nodes, edges)
	}
}
