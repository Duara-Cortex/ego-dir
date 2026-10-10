//go:build pending

// Acceptance tests for the LSP-backed SymbolSource. They run the real
// lsp.Client and graph.Build against the lspfake sample scenario.
// Remove the build constraint above once the code under test exists.
package lspsource_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rasJacobCharles/ego-dir/internal/graph"
	"github.com/rasJacobCharles/ego-dir/internal/lsp"
	"github.com/rasJacobCharles/ego-dir/internal/lsp/lspfake"
	"github.com/rasJacobCharles/ego-dir/internal/lspsource"
)

var defaultKinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports}

type fixture struct {
	src  *lspsource.Source
	root string
	uri  string
}

func setup(t *testing.T) fixture {
	t.Helper()
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := lspfake.New()
	lspfake.Sample(s)
	clientEnd, serverEnd := net.Pipe()
	go s.Serve(context.Background(), serverEnd)
	t.Cleanup(func() { clientEnd.Close(); serverEnd.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := lsp.NewClient(ctx, clientEnd, root)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{
		src:  lspsource.New(c, root),
		root: root,
		uri:  lsp.FileURI(filepath.Join(root, lspfake.SampleFile)),
	}
}

func (f fixture) build(t *testing.T, line, col int, opts graph.Options) *graph.EgoGraph {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// line/col are 1-based editor coordinates; the fixture is ASCII.
	g, err := graph.Build(ctx, f.src, f.uri, line-1, col-1, opts)
	if err != nil {
		t.Fatalf("Build(%d:%d): %v", line, col, err)
	}
	if err := g.Validate(); err != nil {
		t.Fatalf("invalid graph: %v", err)
	}
	return g
}

// summary renders the graph as "name@line hop" nodes and
// "name@line -kind-> name@line xN" edges.
func summary(g *graph.EgoGraph) (nodes, edges []string) {
	label := map[graph.NodeID]string{}
	for _, n := range g.Nodes {
		label[n.ID] = n.Name + "@" + itoa(n.Selection.StartLine)
		nodes = append(nodes, label[n.ID]+" hop"+itoa(n.Hop))
	}
	for _, e := range g.Edges {
		edges = append(edges, label[e.From]+" -"+string(e.Kind)+"-> "+label[e.To]+" x"+itoa(e.Weight))
	}
	sort.Strings(edges)
	return nodes, edges
}

func itoa(i int) string {
	if i < 0 {
		return "-" + itoa(-i)
	}
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

func TestSourceImplementsSymbolSource(t *testing.T) {
	var _ graph.SymbolSource = (*lspsource.Source)(nil)
}

func TestEgoNode(t *testing.T) {
	f := setup(t)
	g := f.build(t, 18, 6, graph.Options{Depth: 1, Kinds: defaultKinds, Body: graph.BodyEgo})
	ego := g.Nodes[0]
	want := graph.Node{
		ID:        graph.NewNodeID(f.uri, graph.Range{StartLine: 18, StartCol: 6, EndLine: 18, EndCol: 15}),
		Name:      "TotalArea",
		Kind:      graph.KindFunction,
		URI:       f.uri,
		Range:     graph.Range{StartLine: 18, StartCol: 1, EndLine: 24, EndCol: 2},
		Selection: graph.Range{StartLine: 18, StartCol: 6, EndLine: 18, EndCol: 15},
		Signature: "func TotalArea(shapes []Shape) float64",
		Doc:       "TotalArea sums the area of every shape.",
		Body: "func TotalArea(shapes []Shape) float64 {\n\tvar total float64\n\tfor _, s := range shapes {\n" +
			"\t\ttotal += s.Area()\n\t}\n\treturn total\n}",
		Hop: 0,
	}
	ego.Qualified = "" // server-specific; not pinned
	if !reflect.DeepEqual(ego, want) {
		t.Errorf("ego node:\n got %+v\nwant %+v", ego, want)
	}
}

func TestEgoFromCallSite(t *testing.T) {
	f := setup(t)
	// Line 28 col 9 is "TotalArea" inside Report: the ego is the callee.
	g := f.build(t, 28, 9, graph.Options{Depth: 1, Kinds: defaultKinds})
	if g.Nodes[0].Name != "TotalArea" || g.Nodes[0].Selection.StartLine != 18 {
		t.Errorf("ego = %s@%d, want TotalArea@18", g.Nodes[0].Name, g.Nodes[0].Selection.StartLine)
	}
}

func TestCallsDepthTwo(t *testing.T) {
	f := setup(t)
	g := f.build(t, 18, 6, graph.Options{Depth: 2, Kinds: defaultKinds})
	nodes, edges := summary(g)
	wantNodes := []string{"TotalArea@18 hop0", "Area@5 hop1", "Report@27 hop1", "Area@12 hop2"}
	if !reflect.DeepEqual(nodes, wantNodes) {
		t.Errorf("nodes = %v\nwant    %v", nodes, wantNodes)
	}
	wantEdges := []string{
		"Area@12 -implements-> Area@5 x1",
		"Report@27 -calls-> TotalArea@18 x1",
		"TotalArea@18 -calls-> Area@5 x1",
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %v\nwant    %v", edges, wantEdges)
	}
	for _, n := range g.Nodes {
		if n.Hop > 0 && n.Body != "" {
			t.Errorf("%s has a body; only the ego should by default", n.Name)
		}
		if n.Signature == "" {
			t.Errorf("%s has no signature", n.Name)
		}
	}
}

func TestCallWeights(t *testing.T) {
	f := setup(t)
	// Report calls NewRect twice.
	g := f.build(t, 27, 6, graph.Options{Depth: 1, Kinds: []graph.EdgeKind{graph.EdgeCalls}, Direction: graph.DirOut})
	_, edges := summary(g)
	want := []string{"Report@27 -calls-> NewRect@15 x2", "Report@27 -calls-> TotalArea@18 x1"}
	if !reflect.DeepEqual(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}

func TestImplementsOrientation(t *testing.T) {
	f := setup(t)
	opts := graph.Options{Depth: 1, Kinds: []graph.EdgeKind{graph.EdgeImplements}}
	for _, at := range []struct{ line, col int }{{9, 6}, {4, 6}} { // Rect, then Shape
		g := f.build(t, at.line, at.col, opts)
		_, edges := summary(g)
		if want := []string{"Rect@9 -implements-> Shape@4 x1"}; !reflect.DeepEqual(edges, want) {
			t.Errorf("from %d:%d edges = %v, want %v (concrete -> interface)", at.line, at.col, edges, want)
		}
		kinds := map[string]graph.SymbolKind{}
		for _, n := range g.Nodes {
			kinds[n.Name] = n.Kind
		}
		if kinds["Rect"] != graph.KindStruct || kinds["Shape"] != graph.KindInterface {
			t.Errorf("kinds = %v", kinds)
		}
	}
}

func TestInstantiates(t *testing.T) {
	f := setup(t)
	g := f.build(t, 15, 6, graph.Options{Depth: 1, Kinds: []graph.EdgeKind{graph.EdgeInstantiates}})
	_, edges := summary(g)
	if want := []string{"NewRect@15 -instantiates-> Rect@9 x1"}; !reflect.DeepEqual(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}

func TestReferencesGroupedByEnclosingSymbol(t *testing.T) {
	f := setup(t)
	g := f.build(t, 15, 6, graph.Options{Depth: 1, Kinds: []graph.EdgeKind{graph.EdgeReferences}})
	_, edges := summary(g)
	if want := []string{"Report@27 -references-> NewRect@15 x2"}; !reflect.DeepEqual(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}

func TestNoAbsolutePathInNames(t *testing.T) {
	f := setup(t)
	g := f.build(t, 18, 6, graph.Options{Depth: 2, Kinds: defaultKinds, Body: graph.BodyAll})
	if len(g.Nodes) != 4 {
		t.Fatalf("got %d nodes, want 4", len(g.Nodes))
	}
	for _, n := range g.Nodes {
		for _, s := range []string{n.Name, n.Signature, n.Doc, n.Body} {
			if strings.Contains(s, f.root) {
				t.Errorf("%s leaks the workspace path: %q", n.Name, s)
			}
		}
	}
}

func TestMissingFile(t *testing.T) {
	f := setup(t)
	missing := lsp.FileURI(filepath.Join(f.root, "missing.go"))
	if _, err := os.Stat(filepath.Join(f.root, "missing.go")); err == nil {
		t.Fatal("fixture unexpectedly has missing.go")
	}
	_, err := graph.Build(context.Background(), f.src, missing, 0, 0, graph.Options{Depth: 1, Kinds: defaultKinds})
	if err == nil {
		t.Error("Build on a missing file succeeded")
	}
}
