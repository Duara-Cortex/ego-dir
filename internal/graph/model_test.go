// Acceptance tests for the graph data structures.
package graph_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
)

const uriA = "file:///ws/a.go"
const uriB = "file:///ws/b.go"

func node(uri string, line, col int, name string, hop int) graph.Node {
	sel := graph.Range{StartLine: line, StartCol: col, EndLine: line, EndCol: col + len(name)}
	return graph.Node{
		ID:        graph.NewNodeID(uri, sel),
		Name:      name,
		Kind:      graph.KindFunction,
		URI:       uri,
		Range:     graph.Range{StartLine: line, StartCol: 1, EndLine: line + 2, EndCol: 2},
		Selection: sel,
		Hop:       hop,
	}
}

func TestNewNodeIDFormat(t *testing.T) {
	id := graph.NewNodeID(uriA, graph.Range{StartLine: 18, StartCol: 6, EndLine: 18, EndCol: 15})
	if id != "file:///ws/a.go#L18:6" {
		t.Errorf("NewNodeID = %q, want %q", id, "file:///ws/a.go#L18:6")
	}
	other := graph.NewNodeID(uriA, graph.Range{StartLine: 18, StartCol: 7, EndLine: 18, EndCol: 15})
	if id == other {
		t.Error("NewNodeID is not unique per selection start")
	}
}

func TestEdgeKinds(t *testing.T) {
	all := []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports, graph.EdgeReferences}
	want := []string{"calls", "implements", "instantiates", "imports", "references"}
	for i, k := range all {
		if string(k) != want[i] {
			t.Errorf("edge kind %d = %q, want %q", i, k, want[i])
		}
	}
}

func TestParseEdgeKinds(t *testing.T) {
	tests := []struct {
		in   string
		want []graph.EdgeKind
	}{
		{"calls", []graph.EdgeKind{graph.EdgeCalls}},
		{" implements , calls ", []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements}},
		{"calls,calls", []graph.EdgeKind{graph.EdgeCalls}},
		{"all", []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports, graph.EdgeReferences}},
		{"imports,references,instantiates", []graph.EdgeKind{graph.EdgeInstantiates, graph.EdgeImports, graph.EdgeReferences}},
	}
	for _, tc := range tests {
		got, err := graph.ParseEdgeKinds(tc.in)
		if err != nil {
			t.Errorf("ParseEdgeKinds(%q) error: %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseEdgeKinds(%q) = %v, want %v (canonical order, no duplicates)", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "call", "calls,,implements", "calls,bogus"} {
		if _, err := graph.ParseEdgeKinds(bad); err == nil {
			t.Errorf("ParseEdgeKinds(%q) succeeded, want error", bad)
		}
	}
	_, err := graph.ParseEdgeKinds("calls,bogus")
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error %v should name the unknown kind", err)
	}
}

func TestAddNodeDedupesKeepingLowestHop(t *testing.T) {
	g := &graph.EgoGraph{}
	far := node(uriA, 10, 6, "F", 2)
	near := far
	near.Hop = 1
	g.AddNode(far)
	g.AddNode(near)
	g.AddNode(far)
	if len(g.Nodes) != 1 {
		t.Fatalf("len(Nodes) = %d, want 1", len(g.Nodes))
	}
	if g.Nodes[0].Hop != 1 {
		t.Errorf("Hop = %d, want lowest hop 1", g.Nodes[0].Hop)
	}
}

func TestAddEdgeDedupes(t *testing.T) {
	a, b := node(uriA, 1, 6, "A", 0), node(uriA, 5, 6, "B", 1)
	g := &graph.EgoGraph{Ego: a.ID}
	g.AddNode(a)
	g.AddNode(b)
	g.AddEdge(graph.Edge{From: a.ID, To: b.ID, Kind: graph.EdgeCalls, Weight: 2})
	g.AddEdge(graph.Edge{From: a.ID, To: b.ID, Kind: graph.EdgeCalls, Weight: 1}) // same edge seen from the other end
	g.AddEdge(graph.Edge{From: a.ID, To: b.ID, Kind: graph.EdgeReferences})       // different kind, weight defaults to 1
	g.AddEdge(graph.Edge{From: b.ID, To: a.ID, Kind: graph.EdgeCalls, Weight: 1}) // different direction
	g.Sort()
	want := []graph.Edge{
		{From: a.ID, To: b.ID, Kind: graph.EdgeCalls, Weight: 2},
		{From: a.ID, To: b.ID, Kind: graph.EdgeReferences, Weight: 1},
		{From: b.ID, To: a.ID, Kind: graph.EdgeCalls, Weight: 1},
	}
	if !reflect.DeepEqual(g.Edges, want) {
		t.Errorf("Edges = %+v\nwant  %+v", g.Edges, want)
	}
}

func TestSortOrder(t *testing.T) {
	ego := node(uriB, 20, 6, "Ego", 0)
	b9 := node(uriB, 9, 6, "B9", 1)
	b30 := node(uriB, 30, 6, "B30", 1)
	a50 := node(uriA, 50, 6, "A50", 1)
	two := node(uriA, 1, 6, "Two", 2)
	g := &graph.EgoGraph{Ego: ego.ID, Depth: 2}
	for _, n := range []graph.Node{two, b30, ego, b9, a50} {
		g.AddNode(n)
	}
	// Line 9 must sort before line 30 numerically, not as strings.
	g.AddEdge(graph.Edge{From: b30.ID, To: ego.ID, Kind: graph.EdgeCalls})
	g.AddEdge(graph.Edge{From: ego.ID, To: two.ID, Kind: graph.EdgeImplements})
	g.AddEdge(graph.Edge{From: ego.ID, To: two.ID, Kind: graph.EdgeCalls})
	g.AddEdge(graph.Edge{From: b9.ID, To: ego.ID, Kind: graph.EdgeCalls})
	g.AddEdge(graph.Edge{From: a50.ID, To: b9.ID, Kind: graph.EdgeCalls})
	g.Sort()

	var names []string
	for _, n := range g.Nodes {
		names = append(names, n.Name)
	}
	// (Hop, URI, StartLine, StartCol)
	if want := []string{"Ego", "A50", "B9", "B30", "Two"}; !reflect.DeepEqual(names, want) {
		t.Errorf("node order = %v, want %v", names, want)
	}

	// Edges by From location (URI, line, col), then To location, then Kind.
	wantEdges := []graph.Edge{
		{From: a50.ID, To: b9.ID, Kind: graph.EdgeCalls, Weight: 1},
		{From: b9.ID, To: ego.ID, Kind: graph.EdgeCalls, Weight: 1},
		{From: ego.ID, To: two.ID, Kind: graph.EdgeCalls, Weight: 1},
		{From: ego.ID, To: two.ID, Kind: graph.EdgeImplements, Weight: 1},
		{From: b30.ID, To: ego.ID, Kind: graph.EdgeCalls, Weight: 1},
	}
	if !reflect.DeepEqual(g.Edges, wantEdges) {
		t.Errorf("edge order:\n got %+v\nwant %+v", g.Edges, wantEdges)
	}
}

func TestValidate(t *testing.T) {
	ego, alt := node(uriA, 1, 6, "Ego", 0), node(uriA, 9, 6, "Alt", 1)
	valid := func() *graph.EgoGraph {
		g := &graph.EgoGraph{Ego: ego.ID, Depth: 1}
		g.AddNode(ego)
		g.AddNode(alt)
		g.AddEdge(graph.Edge{From: alt.ID, To: ego.ID, Kind: graph.EdgeCalls})
		g.Sort()
		return g
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate(valid graph) = %v", err)
	}

	broken := map[string]func(g *graph.EgoGraph){
		"ego not in nodes":     func(g *graph.EgoGraph) { g.Ego = "file:///nowhere#L1:1" },
		"edge to unknown node": func(g *graph.EgoGraph) { g.Edges[0].To = "file:///nowhere#L1:1" },
		"ego hop not zero":     func(g *graph.EgoGraph) { g.Nodes[0].Hop = 1 },
		"alter hop zero":       func(g *graph.EgoGraph) { g.Nodes[1].Hop = 0 },
		"hop beyond depth":     func(g *graph.EgoGraph) { g.Nodes[1].Hop = 2 },
		"unknown edge kind":    func(g *graph.EgoGraph) { g.Edges[0].Kind = "likes" },
		"zero weight":          func(g *graph.EgoGraph) { g.Edges[0].Weight = 0 },
	}
	for name, mutate := range broken {
		g := valid()
		mutate(g)
		if err := g.Validate(); err == nil {
			t.Errorf("Validate(%s) = nil, want error", name)
		}
	}
}

func TestAddNodeKeepsFirstNodeData(t *testing.T) {
	g := &graph.EgoGraph{}
	first := node(uriA, 3, 6, "Fn", 2)
	first.Signature = "func Fn()"
	later := first
	later.Signature = "changed"
	later.Hop = 1
	g.AddNode(first)
	g.AddNode(later)
	if g.Nodes[0].Signature != "func Fn()" || g.Nodes[0].Hop != 1 {
		t.Errorf("node = %+v, want the first node's data with the lower hop", g.Nodes[0])
	}
}

func TestSortEdgesWithUnknownNodes(t *testing.T) {
	// Edges that point outside the graph (invalid, but Sort must still be
	// deterministic) fall back to ID order.
	ego := node(uriA, 1, 6, "Ego", 0)
	g := &graph.EgoGraph{Ego: ego.ID}
	g.AddNode(ego)
	g.AddEdge(graph.Edge{From: "z#L1:1", To: ego.ID, Kind: graph.EdgeCalls})
	g.AddEdge(graph.Edge{From: "a#L1:1", To: ego.ID, Kind: graph.EdgeCalls})
	g.Sort()
	if g.Edges[0].From != "a#L1:1" || g.Edges[1].From != "z#L1:1" {
		t.Errorf("edges = %+v, want ID order", g.Edges)
	}
}

func TestValidateDuplicateNodes(t *testing.T) {
	ego := node(uriA, 1, 6, "Ego", 0)
	g := &graph.EgoGraph{Ego: ego.ID, Nodes: []graph.Node{ego, ego}}
	if err := g.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("Validate = %v, want a duplicate node error", err)
	}
}
