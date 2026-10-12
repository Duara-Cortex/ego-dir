// Acceptance tests for the LSP-backed SymbolSource. They run the real
// lsp.Client and graph.Build against the lspfake sample scenario.
package lspsource_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
	"github.com/Duara-Cortex/ego-dir/internal/lspsource"
)

var defaultKinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports}

type fixture struct {
	closeServer func()
	src         *lspsource.Source
	root        string
	uri         string
}

func setup(t *testing.T) fixture { return setupWith(t, nil) }

// setupWith is setup with a hook to change the fake server's answers. The
// hook gets the URI of shapes.go.
func setupWith(t *testing.T, customise func(s *lspfake.Server, uri string)) fixture {
	t.Helper()
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := lspfake.New()
	lspfake.Sample(s)
	if customise != nil {
		customise(s, lsp.FileURI(filepath.Join(root, lspfake.SampleFile)))
	}
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
		closeServer: func() { serverEnd.Close() },
		src:         lspsource.New(c, root),
		root:        root,
		uri:         lsp.FileURI(filepath.Join(root, lspfake.SampleFile)),
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

func TestCallsOfNodesFoundByLocation(t *testing.T) {
	f := setup(t)
	// TotalArea is reached through references (no call hierarchy item yet),
	// so expanding its callers needs a prepareCallHierarchy first.
	g := f.build(t, 5, 2, graph.Options{Depth: 2, Kinds: []graph.EdgeKind{graph.EdgeCalls, graph.EdgeReferences}, Direction: graph.DirIn})
	nodes, edges := summary(g)
	if want := []string{"Area@5 hop0", "TotalArea@18 hop1", "Report@27 hop2"}; !reflect.DeepEqual(nodes, want) {
		t.Errorf("nodes = %v, want %v", nodes, want)
	}
	want := []string{
		"Report@27 -calls-> TotalArea@18 x1",
		"Report@27 -references-> TotalArea@18 x1",
		"TotalArea@18 -calls-> Area@5 x1",
		"TotalArea@18 -references-> Area@5 x1",
	}
	if !reflect.DeepEqual(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}

func TestNoCallsForTypes(t *testing.T) {
	f := setup(t)
	g := f.build(t, 4, 6, graph.Options{Depth: 1, Kinds: []graph.EdgeKind{graph.EdgeCalls}})
	if len(g.Nodes) != 1 || len(g.Edges) != 0 {
		t.Errorf("Shape has call edges: %+v", g)
	}
	if g.Nodes[0].Signature != "type Shape interface {\n\tArea() float64\n}" || g.Nodes[0].Qualified != "" {
		t.Errorf("Shape node = %+v", g.Nodes[0])
	}
}

func TestTypeNodeCoversDeclaration(t *testing.T) {
	// gopls starts a type's range at its name; the node starts at "type".
	f := setup(t)
	g := f.build(t, 4, 6, graph.Options{Depth: 1, Kinds: defaultKinds, Body: graph.BodyEgo})
	ego := g.Nodes[0]
	if want := (graph.Range{StartLine: 4, StartCol: 1, EndLine: 6, EndCol: 2}); ego.Range != want {
		t.Errorf("Shape range = %+v, want %+v", ego.Range, want)
	}
	if want := "type Shape interface {\n\tArea() float64\n}"; ego.Body != want {
		t.Errorf("Shape body = %q, want %q", ego.Body, want)
	}
}

func TestHoverIsCleanedUp(t *testing.T) {
	f := setup(t)
	g := f.build(t, 9, 6, graph.Options{Depth: 2, Kinds: defaultKinds}) // Rect
	docs := map[string]string{}
	for _, n := range g.Nodes {
		docs[n.Name+"@"+itoa(n.Selection.StartLine)] = n.Doc
		for _, s := range []string{n.Signature, n.Doc} {
			for _, junk := range []string{"---", "size=", "pkg.go.dev", "```"} {
				if strings.Contains(s, junk) {
					t.Errorf("%s: %q contains %q", n.Name, s, junk)
				}
			}
		}
	}
	want := map[string]string{"Rect@9": "Rect is an axis-aligned rectangle.", "Shape@4": "Shape is anything with an area."}
	for k, v := range want {
		if docs[k] != v {
			t.Errorf("doc of %s = %q, want %q", k, docs[k], v)
		}
	}
}

func TestNeighboursOutsideTheRootAreDropped(t *testing.T) {
	// gopls points into $GOROOT (float64, fmt.Errorf, ...). Those files are
	// outside the workspace; they are neither read nor reported.
	outside := map[string]any{"uri": lspfake.SampleBuiltinURI, "range": map[string]any{
		"start": map[string]int{"line": 59, "character": 5}, "end": map[string]int{"line": 59, "character": 12}}}
	f := setupWith(t, func(s *lspfake.Server, _ string) {
		s.Result("callHierarchy/outgoingCalls", []any{map[string]any{
			"to": map[string]any{"name": "Errorf", "kind": lspfake.KindFunction, "uri": lspfake.SampleBuiltinURI,
				"range": outside["range"], "selectionRange": outside["range"]},
			"fromRanges": []any{outside["range"]},
		}})
		s.Result("textDocument/references", []any{outside})
	})
	all := []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeReferences}
	g := f.build(t, 18, 6, graph.Options{Depth: 2, Kinds: all})
	nodes, edges := summary(g)
	if want := []string{"TotalArea@18 hop0", "Report@27 hop1"}; !reflect.DeepEqual(nodes, want) {
		t.Errorf("nodes = %v, want %v", nodes, want)
	}
	if want := []string{"Report@27 -calls-> TotalArea@18 x1"}; !reflect.DeepEqual(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}

func TestOutOfRootTypeDefinitionIsNotAnInstantiation(t *testing.T) {
	// TotalArea returns float64, which gopls places in $GOROOT.
	f := setup(t)
	g := f.build(t, 18, 6, graph.Options{Depth: 1, Kinds: []graph.EdgeKind{graph.EdgeInstantiates}})
	if len(g.Nodes) != 1 || len(g.Edges) != 0 {
		nodes, edges := summary(g)
		t.Errorf("graph = %v %v, want only the ego", nodes, edges)
	}
}

func TestMethodNodesComeFromTheOutline(t *testing.T) {
	// gopls gives call hierarchy items kind Function and the name as their
	// range; kind, range and qualified name come from the outline instead.
	f := setup(t)
	g := f.build(t, 18, 6, graph.Options{Depth: 2, Kinds: defaultKinds})
	byLine := map[int]graph.Node{}
	for _, n := range g.Nodes {
		byLine[n.Selection.StartLine] = n
	}
	tests := []struct {
		line      int
		kind      graph.SymbolKind
		qualified string
		rng       graph.Range
	}{
		{5, graph.KindMethod, "Shape.Area", graph.Range{StartLine: 5, StartCol: 2, EndLine: 5, EndCol: 16}},
		{12, graph.KindMethod, "Rect.Area", graph.Range{StartLine: 12, StartCol: 1, EndLine: 12, EndCol: 50}},
		{27, graph.KindFunction, "", graph.Range{StartLine: 27, StartCol: 1, EndLine: 29, EndCol: 2}},
	}
	for _, tc := range tests {
		n, ok := byLine[tc.line]
		if !ok {
			t.Errorf("no node at line %d", tc.line)
			continue
		}
		if n.Kind != tc.kind || n.Qualified != tc.qualified || n.Range != tc.rng {
			t.Errorf("line %d: kind %s, qualified %q, range %+v; want %s, %q, %+v",
				tc.line, n.Kind, n.Qualified, n.Range, tc.kind, tc.qualified, tc.rng)
		}
	}
}

func TestCallerThroughDynamicDispatch(t *testing.T) {
	// gopls lists TotalArea as a caller of Rect.Area, as it calls Shape.Area.
	f := setup(t)
	g := f.build(t, 12, 15, graph.Options{Depth: 1, Kinds: []graph.EdgeKind{graph.EdgeCalls}, Direction: graph.DirIn})
	_, edges := summary(g)
	if want := []string{"TotalArea@18 -calls-> Area@12 x1"}; !reflect.DeepEqual(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}

func TestReferencesOfAType(t *testing.T) {
	f := setup(t)
	g := f.build(t, 9, 6, graph.Options{Depth: 1, Kinds: []graph.EdgeKind{graph.EdgeReferences}})
	_, edges := summary(g)
	want := []string{"Area@12 -references-> Rect@9 x1", "NewRect@15 -references-> Rect@9 x2"}
	if !reflect.DeepEqual(edges, want) {
		t.Errorf("edges = %v, want %v", edges, want)
	}
}

func TestQualifiedNameFromOutlineParent(t *testing.T) {
	f := setup(t)
	g := f.build(t, 5, 2, graph.Options{Depth: 1, Kinds: []graph.EdgeKind{graph.EdgeCalls}})
	if g.Nodes[0].Qualified != "Shape.Area" {
		t.Errorf("Qualified = %q, want Shape.Area", g.Nodes[0].Qualified)
	}
}

func TestServerErrorsMeanNoEdges(t *testing.T) {
	f := setupWith(t, func(s *lspfake.Server, _ string) {
		for _, m := range []string{"textDocument/typeDefinition", "textDocument/implementation",
			"textDocument/references", "callHierarchy/incomingCalls", "callHierarchy/outgoingCalls"} {
			s.Handle(m, func(context.Context, json.RawMessage) (any, error) {
				return nil, &lspfake.Error{Code: -32603, Message: "not applicable"}
			})
		}
	})
	g := f.build(t, 15, 6, graph.Options{Depth: 2, Kinds: []graph.EdgeKind{
		graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeReferences}})
	if len(g.Nodes) != 1 || len(g.Edges) != 0 {
		t.Errorf("graph = %+v, want only the ego", g)
	}
}

func TestBrokenSessionIsAnError(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ego, err := f.src.Resolve(ctx, f.uri, 17, 5)
	if err != nil {
		t.Fatal(err)
	}
	f.closeServer()
	if _, err := f.src.Neighbors(ctx, ego, graph.DirIn, []graph.EdgeKind{graph.EdgeCalls}); err == nil {
		t.Error("Neighbors succeeded after the server went away")
	}
}

func TestNeighborsOfUnknownNode(t *testing.T) {
	f := setup(t)
	_, err := f.src.Neighbors(context.Background(), graph.Node{ID: "file:///x#L1:1"}, graph.DirIn, []graph.EdgeKind{graph.EdgeCalls})
	if err == nil {
		t.Error("Neighbors accepted a node the source never returned")
	}
}

func TestResolveNothingThere(t *testing.T) {
	f := setup(t)
	if _, err := f.src.Resolve(context.Background(), f.uri, 1, 0); err == nil || !strings.Contains(err.Error(), "no symbol") {
		t.Errorf("Resolve(blank line) = %v, want a no-symbol error", err)
	}
}

func TestResolveOutsideOutline(t *testing.T) {
	// A definition that is not in the outline (a local variable) still
	// gives a node, named by its source text.
	f := setupWith(t, func(s *lspfake.Server, uri string) {
		s.Result("textDocument/definition", map[string]any{"uri": uri, "range": map[string]any{
			"start": map[string]int{"line": 18, "character": 5}, "end": map[string]int{"line": 18, "character": 10}}})
	})
	n, err := f.src.Resolve(context.Background(), f.uri, 18, 6)
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "total" || n.Kind != graph.KindVariable || n.Selection.StartLine != 19 {
		t.Errorf("node = %+v, want variable total at line 19", n)
	}
}
