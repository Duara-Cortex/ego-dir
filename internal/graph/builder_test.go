//go:build pending

// Acceptance tests for the graph builder.
// Remove the build constraint above once the code under test exists.
package graph_test

import (
	"context"
	"errors"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/rasJacobCharles/ego-dir/internal/graph"
)

// fakeSource is an in-memory SymbolSource over a fixed set of edges.
// It deliberately ignores the kinds filter, so the builder must filter.
type fakeSource struct {
	nodes    map[string]graph.Node // by name
	edges    []fakeEdge
	shuffle  *rand.Rand
	failOn   string // node name whose Neighbors call fails
	resolved []string
	expanded map[string]int // Neighbors calls per node name and direction
}

type fakeEdge struct {
	from, to string
	kind     graph.EdgeKind
	weight   int
}

var errSource = errors.New("source failure")

func newSource(edges ...fakeEdge) *fakeSource {
	src := &fakeSource{nodes: map[string]graph.Node{}, edges: edges, expanded: map[string]int{}}
	names := map[string]bool{}
	for _, e := range edges {
		names[e.from], names[e.to] = true, true
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for i, name := range sorted {
		line := 10 * (i + 1)
		sel := graph.Range{StartLine: line, StartCol: 6, EndLine: line, EndCol: 6 + len(name)}
		src.nodes[name] = graph.Node{
			ID:        graph.NewNodeID("file:///ws/x.go", sel),
			Name:      name,
			Kind:      graph.KindFunction,
			URI:       "file:///ws/x.go",
			Range:     graph.Range{StartLine: line, StartCol: 1, EndLine: line + 3, EndCol: 2},
			Selection: sel,
			Signature: "func " + name + "()",
			Body:      "func " + name + "() {}",
		}
	}
	return src
}

func (s *fakeSource) Resolve(ctx context.Context, uri string, line, char int) (graph.Node, error) {
	if err := ctx.Err(); err != nil {
		return graph.Node{}, err
	}
	for _, n := range s.nodes {
		// line/char are 0-based LSP coordinates; Selection is 1-based.
		if n.URI == uri && n.Selection.StartLine == line+1 && n.Selection.StartCol == char+1 {
			s.resolved = append(s.resolved, n.Name)
			return n, nil
		}
	}
	return graph.Node{}, errSource
}

func (s *fakeSource) Neighbors(ctx context.Context, n graph.Node, dir graph.Direction, kinds []graph.EdgeKind) ([]graph.Neighbor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n.Name == s.failOn {
		return nil, errSource
	}
	if dir != graph.DirIn && dir != graph.DirOut {
		return nil, errors.New("Neighbors must be called with DirIn or DirOut")
	}
	key := n.Name + map[graph.Direction]string{graph.DirIn: "/in", graph.DirOut: "/out"}[dir]
	s.expanded[key]++
	var out []graph.Neighbor
	for _, e := range s.edges {
		switch {
		case dir == graph.DirOut && e.from == n.Name:
			out = append(out, graph.Neighbor{Node: s.nodes[e.to], Kind: e.kind, Weight: e.weight})
		case dir == graph.DirIn && e.to == n.Name:
			out = append(out, graph.Neighbor{Node: s.nodes[e.from], Kind: e.kind, Weight: e.weight})
		}
	}
	if s.shuffle != nil {
		s.shuffle.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	}
	return out, nil
}

func calls(from, to string) fakeEdge { return fakeEdge{from, to, graph.EdgeCalls, 1} }

func build(t *testing.T, src *fakeSource, ego string, opts graph.Options) *graph.EgoGraph {
	t.Helper()
	n := src.nodes[ego]
	g, err := graph.Build(context.Background(), src, n.URI, n.Selection.StartLine-1, n.Selection.StartCol-1, opts)
	if err != nil {
		t.Fatalf("Build(%s): %v", ego, err)
	}
	if err := g.Validate(); err != nil {
		t.Fatalf("Build(%s) returned an invalid graph: %v", ego, err)
	}
	return g
}

// hops returns name -> hop for every node.
func hops(src *fakeSource, g *graph.EgoGraph) map[string]int {
	byID := map[graph.NodeID]string{}
	for name, n := range src.nodes {
		byID[n.ID] = name
	}
	out := map[string]int{}
	for _, n := range g.Nodes {
		out[byID[n.ID]] = n.Hop
	}
	return out
}

// edgeSet renders edges as "from -kind-> to (weight)".
func edgeSet(src *fakeSource, g *graph.EgoGraph) []string {
	byID := map[graph.NodeID]string{}
	for name, n := range src.nodes {
		byID[n.ID] = name
	}
	var out []string
	for _, e := range g.Edges {
		out = append(out, byID[e.From]+" -"+string(e.Kind)+"-> "+byID[e.To]+" ("+string(rune('0'+e.Weight))+")")
	}
	sort.Strings(out)
	return out
}

var allKinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports, graph.EdgeReferences}

// chain: X -> R -> T -> A -> B, with T the ego.
func chain() *fakeSource {
	return newSource(calls("X", "R"), calls("R", "T"), calls("T", "A"), calls("A", "B"))
}

func TestDepthOne(t *testing.T) {
	src := chain()
	g := build(t, src, "T", graph.Options{Depth: 1, Kinds: allKinds, Direction: graph.DirBoth})
	if want := map[string]int{"T": 0, "R": 1, "A": 1}; !reflect.DeepEqual(hops(src, g), want) {
		t.Errorf("hops = %v, want %v", hops(src, g), want)
	}
	if want := []string{"R -calls-> T (1)", "T -calls-> A (1)"}; !reflect.DeepEqual(edgeSet(src, g), want) {
		t.Errorf("edges = %v, want %v", edgeSet(src, g), want)
	}
	if g.Ego != src.nodes["T"].ID || g.Depth != 1 || g.Nodes[0].ID != g.Ego {
		t.Errorf("Ego = %q, Depth = %d, first node = %q", g.Ego, g.Depth, g.Nodes[0].ID)
	}
}

func TestDepthTwoAndThree(t *testing.T) {
	src := chain()
	g := build(t, src, "T", graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirBoth})
	if want := map[string]int{"T": 0, "R": 1, "A": 1, "X": 2, "B": 2}; !reflect.DeepEqual(hops(src, g), want) {
		t.Errorf("depth 2 hops = %v, want %v", hops(src, g), want)
	}
	// Nodes at the final hop are not expanded.
	if src.expanded["X/in"] != 0 || src.expanded["B/out"] != 0 {
		t.Errorf("frontier nodes were expanded: %v", src.expanded)
	}

	src = newSource(calls("Y", "X"), calls("X", "R"), calls("R", "T"))
	g = build(t, src, "T", graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirBoth})
	if _, ok := hops(src, g)["Y"]; ok {
		t.Error("depth 2 included a node 3 hops away")
	}
	g = build(t, src, "T", graph.Options{Depth: 3, Kinds: allKinds, Direction: graph.DirBoth})
	if hops(src, g)["Y"] != 3 {
		t.Errorf("depth 3 hops = %v, want Y at 3", hops(src, g))
	}
}

func TestDirectionsDoNotMix(t *testing.T) {
	// S is a sibling: another callee of T's caller R. It is neither upstream
	// nor downstream of T, so it must not appear at depth 2.
	src := newSource(calls("R", "T"), calls("R", "S"), calls("T", "A"), calls("C", "A"))
	g := build(t, src, "T", graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirBoth})
	if want := map[string]int{"T": 0, "R": 1, "A": 1}; !reflect.DeepEqual(hops(src, g), want) {
		t.Errorf("hops = %v, want %v (no siblings or co-callers)", hops(src, g), want)
	}

	g = build(t, src, "T", graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirIn})
	if want := map[string]int{"T": 0, "R": 1}; !reflect.DeepEqual(hops(src, g), want) {
		t.Errorf("DirIn hops = %v, want %v", hops(src, g), want)
	}
	g = build(t, src, "T", graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirOut})
	if want := map[string]int{"T": 0, "A": 1}; !reflect.DeepEqual(hops(src, g), want) {
		t.Errorf("DirOut hops = %v, want %v", hops(src, g), want)
	}
}

func TestImplementsIsFollowedBothWays(t *testing.T) {
	// T calls the interface method I; Impl implements I. Impl is the real
	// downstream target (dynamic dispatch), even though the edge points at I.
	// Upstream of Impl, callers of I (here T) are callers of Impl.
	src := newSource(calls("T", "I"), fakeEdge{"Impl", "I", graph.EdgeImplements, 1}, calls("R", "T"))
	g := build(t, src, "T", graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirOut})
	if want := map[string]int{"T": 0, "I": 1, "Impl": 2}; !reflect.DeepEqual(hops(src, g), want) {
		t.Errorf("downstream hops = %v, want %v", hops(src, g), want)
	}
	if want := []string{"Impl -implements-> I (1)", "T -calls-> I (1)"}; !reflect.DeepEqual(edgeSet(src, g), want) {
		t.Errorf("edges = %v, want %v (edge orientation is preserved)", edgeSet(src, g), want)
	}

	g = build(t, src, "Impl", graph.Options{Depth: 3, Kinds: allKinds, Direction: graph.DirIn})
	if want := map[string]int{"Impl": 0, "I": 1, "T": 2, "R": 3}; !reflect.DeepEqual(hops(src, g), want) {
		t.Errorf("upstream hops = %v, want %v", hops(src, g), want)
	}
}

func TestCyclesTerminate(t *testing.T) {
	src := newSource(calls("A", "B"), calls("B", "A"), calls("B", "B"))
	g := build(t, src, "A", graph.Options{Depth: 5, Kinds: allKinds, Direction: graph.DirBoth})
	if want := map[string]int{"A": 0, "B": 1}; !reflect.DeepEqual(hops(src, g), want) {
		t.Errorf("hops = %v, want %v", hops(src, g), want)
	}
	if want := []string{"A -calls-> B (1)", "B -calls-> A (1)", "B -calls-> B (1)"}; !reflect.DeepEqual(edgeSet(src, g), want) {
		t.Errorf("edges = %v, want %v", edgeSet(src, g), want)
	}
	for key, n := range src.expanded {
		if n > 1 {
			t.Errorf("%s expanded %d times, want at most once", key, n)
		}
	}
}

func TestShortestHopWins(t *testing.T) {
	src := newSource(calls("T", "A"), calls("A", "B"), calls("T", "B"))
	g := build(t, src, "T", graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirOut})
	if want := map[string]int{"T": 0, "A": 1, "B": 1}; !reflect.DeepEqual(hops(src, g), want) {
		t.Errorf("hops = %v, want %v", hops(src, g), want)
	}
	// The edge between two hop-1 nodes is still found when A is expanded.
	if want := []string{"A -calls-> B (1)", "T -calls-> A (1)", "T -calls-> B (1)"}; !reflect.DeepEqual(edgeSet(src, g), want) {
		t.Errorf("edges = %v, want %v", edgeSet(src, g), want)
	}
}

func TestKindFilter(t *testing.T) {
	src := newSource(
		calls("R", "T"),
		fakeEdge{"T", "I", graph.EdgeImplements, 1},
		fakeEdge{"T", "Ty", graph.EdgeInstantiates, 1},
		fakeEdge{"T", "Pkg", graph.EdgeImports, 1},
		fakeEdge{"U", "T", graph.EdgeReferences, 4},
	)
	tests := []struct {
		kinds []graph.EdgeKind
		want  []string
	}{
		{[]graph.EdgeKind{graph.EdgeCalls}, []string{"R -calls-> T (1)"}},
		{[]graph.EdgeKind{graph.EdgeImplements}, []string{"T -implements-> I (1)"}},
		{[]graph.EdgeKind{graph.EdgeInstantiates, graph.EdgeImports}, []string{"T -imports-> Pkg (1)", "T -instantiates-> Ty (1)"}},
		{[]graph.EdgeKind{graph.EdgeReferences}, []string{"U -references-> T (4)"}},
	}
	for _, tc := range tests {
		g := build(t, src, "T", graph.Options{Depth: 1, Kinds: tc.kinds, Direction: graph.DirBoth})
		if got := edgeSet(src, g); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("kinds %v: edges = %v, want %v", tc.kinds, got, tc.want)
		}
		if len(g.Nodes) != len(tc.want)+1 {
			t.Errorf("kinds %v: %d nodes, want %d (no nodes reached through filtered edges)", tc.kinds, len(g.Nodes), len(tc.want)+1)
		}
	}
}

func TestEdgeSeenFromBothEndsIsNotDoubled(t *testing.T) {
	src := newSource(fakeEdge{"T", "A", graph.EdgeCalls, 3}, calls("A", "T"))
	g := build(t, src, "T", graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirBoth})
	if want := []string{"A -calls-> T (1)", "T -calls-> A (3)"}; !reflect.DeepEqual(edgeSet(src, g), want) {
		t.Errorf("edges = %v, want %v", edgeSet(src, g), want)
	}
}

func TestDeterministicUnderShuffledSources(t *testing.T) {
	edges := []fakeEdge{
		calls("R1", "T"), calls("R2", "T"), calls("R3", "R1"), calls("T", "A"), calls("T", "B"),
		calls("A", "C"), calls("B", "C"), fakeEdge{"T", "I", graph.EdgeImplements, 1}, calls("R3", "R2"),
	}
	opts := graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirBoth}
	want := build(t, newSource(edges...), "T", opts)
	if len(want.Nodes) != 8 || len(want.Edges) != 9 {
		t.Fatalf("got %d nodes and %d edges, want 8 and 9", len(want.Nodes), len(want.Edges))
	}
	for seed := int64(1); seed <= 25; seed++ {
		src := newSource(edges...)
		src.shuffle = rand.New(rand.NewSource(seed))
		if got := build(t, src, "T", opts); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d produced a different graph:\n got %+v\nwant %+v", seed, got, want)
		}
	}
	// Sorted as documented: (Hop, URI, StartLine, StartCol).
	for i := 1; i < len(want.Nodes); i++ {
		a, b := want.Nodes[i-1], want.Nodes[i]
		if a.Hop > b.Hop || (a.Hop == b.Hop && a.Selection.StartLine > b.Selection.StartLine) {
			t.Errorf("nodes not sorted at %d: %s (hop %d) before %s (hop %d)", i, a.Name, a.Hop, b.Name, b.Hop)
		}
	}
}

func TestBodies(t *testing.T) {
	src := chain()
	bodies := func(mode graph.BodyMode) map[string]bool {
		g := build(t, src, "T", graph.Options{Depth: 1, Kinds: allKinds, Direction: graph.DirBoth, Body: mode})
		out := map[string]bool{}
		for _, n := range g.Nodes {
			out[n.Name] = n.Body != ""
		}
		return out
	}
	if got, want := bodies(graph.BodyEgo), map[string]bool{"T": true, "R": false, "A": false}; !reflect.DeepEqual(got, want) {
		t.Errorf("BodyEgo = %v, want %v", got, want)
	}
	if got, want := bodies(graph.BodyAll), map[string]bool{"T": true, "R": true, "A": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("BodyAll = %v, want %v", got, want)
	}
	if got, want := bodies(graph.BodyNone), map[string]bool{"T": false, "R": false, "A": false}; !reflect.DeepEqual(got, want) {
		t.Errorf("BodyNone = %v, want %v", got, want)
	}
	// Signatures are always kept.
	g := build(t, src, "T", graph.Options{Depth: 1, Kinds: allKinds, Direction: graph.DirBoth, Body: graph.BodyNone})
	for _, n := range g.Nodes {
		if n.Signature == "" {
			t.Errorf("%s lost its signature", n.Name)
		}
	}
}

func TestInvalidOptions(t *testing.T) {
	src := chain()
	n := src.nodes["T"]
	for _, opts := range []graph.Options{
		{Depth: 0, Kinds: allKinds},
		{Depth: 6, Kinds: allKinds},
		{Depth: -1, Kinds: allKinds},
		{Depth: 1, Kinds: nil},
	} {
		if _, err := graph.Build(context.Background(), src, n.URI, 9, 5, opts); err == nil {
			t.Errorf("Build(%+v) succeeded, want error", opts)
		}
	}
	if len(src.resolved) != 0 {
		t.Error("Build called the source despite invalid options")
	}
}

func TestErrorsPropagate(t *testing.T) {
	src := chain()
	if _, err := graph.Build(context.Background(), src, "file:///ws/x.go", 0, 0, graph.Options{Depth: 1, Kinds: allKinds}); !errors.Is(err, errSource) {
		t.Errorf("Resolve failure: error = %v, want it to wrap the source error", err)
	}
	src.failOn = "A"
	n := src.nodes["T"]
	_, err := graph.Build(context.Background(), src, n.URI, n.Selection.StartLine-1, n.Selection.StartCol-1, graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirBoth})
	if !errors.Is(err, errSource) {
		t.Errorf("Neighbors failure: error = %v, want it to wrap the source error", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = graph.Build(ctx, chain(), n.URI, n.Selection.StartLine-1, n.Selection.StartCol-1, graph.Options{Depth: 1, Kinds: allKinds})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: error = %v, want context.Canceled", err)
	}
}
