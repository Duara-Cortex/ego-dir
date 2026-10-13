// Package graph defines the ego-graph model: a directed, weighted subgraph
// of code symbols centred on one symbol (the ego).
package graph

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// NodeID identifies a node: "<uri>#L<line>:<col>" of a symbol's name,
// 1-based, or "module:<path>" for a module.
type NodeID string

// NewNodeID returns the ID of the symbol whose name starts at sel.
func NewNodeID(uri string, sel Range) NodeID {
	return NodeID(fmt.Sprintf("%s#L%d:%d", uri, sel.StartLine, sel.StartCol))
}

// NewModuleID returns the ID of the module that importers name path. It
// does not depend on where the module is stored.
func NewModuleID(path string) NodeID {
	return NodeID("module:" + path)
}

// Range is a span in a file. Lines and columns are 1-based (as in editors)
// and columns count characters. The end column is exclusive.
type Range struct {
	StartLine, StartCol, EndLine, EndCol int
}

// SymbolKind is a language-neutral kind of symbol.
type SymbolKind string

// Symbol kinds.
const (
	KindFunction  SymbolKind = "function"
	KindMethod    SymbolKind = "method"
	KindStruct    SymbolKind = "struct"
	KindClass     SymbolKind = "class"
	KindInterface SymbolKind = "interface"
	KindType      SymbolKind = "type"
	KindVariable  SymbolKind = "variable"
	KindConstant  SymbolKind = "constant"
	KindField     SymbolKind = "field"
	KindModule    SymbolKind = "module"
	KindOther     SymbolKind = "other"
)

// Node is one symbol in an ego-graph. A module node has no Range or
// Selection; its URI is its directory when that is in the workspace, and
// empty otherwise.
type Node struct {
	ID        NodeID
	Name      string // e.g. "Build"
	Qualified string // e.g. "graph.Builder.Build", when known
	Kind      SymbolKind
	URI       string
	Range     Range // the whole declaration
	Selection Range // the symbol's name
	Signature string
	Doc       string
	Body      string // source of the declaration; empty unless requested
	Hop       int    // distance from the ego; 0 for the ego itself
}

// Located reports whether the node has a position in a file. Module nodes
// do not.
func (n Node) Located() bool { return n.Selection.StartLine > 0 }

// EdgeKind is the relationship an edge represents.
type EdgeKind string

// Edge kinds, in canonical order.
const (
	EdgeCalls        EdgeKind = "calls"
	EdgeImplements   EdgeKind = "implements"
	EdgeInstantiates EdgeKind = "instantiates"
	EdgeImports      EdgeKind = "imports"
	EdgeContains     EdgeKind = "contains"
	EdgeReferences   EdgeKind = "references"
)

var edgeKinds = []EdgeKind{EdgeCalls, EdgeImplements, EdgeInstantiates, EdgeImports, EdgeContains, EdgeReferences}

// rank returns the kind's position in canonical order, or -1 if unknown.
func (k EdgeKind) rank() int {
	for i, known := range edgeKinds {
		if k == known {
			return i
		}
	}
	return -1
}

// ParseEdgeKinds parses a comma-separated list of edge kinds, or "all".
// The result is in canonical order without duplicates.
func ParseEdgeKinds(s string) ([]EdgeKind, error) {
	if strings.TrimSpace(s) == "all" {
		return append([]EdgeKind(nil), edgeKinds...), nil
	}
	seen := make([]bool, len(edgeKinds))
	for _, part := range strings.Split(s, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, fmt.Errorf("empty edge kind in %q", s)
		}
		r := EdgeKind(name).rank()
		if r < 0 {
			return nil, fmt.Errorf("unknown edge kind %q (want %s or all)", name, joinKinds(edgeKinds))
		}
		seen[r] = true
	}
	var out []EdgeKind
	for i, ok := range seen {
		if ok {
			out = append(out, edgeKinds[i])
		}
	}
	return out, nil
}

func joinKinds(kinds []EdgeKind) string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

// Edge is a directed relationship From -> To. Weight counts the call sites
// or references it stands for, and is at least 1.
type Edge struct {
	From, To NodeID
	Kind     EdgeKind
	Weight   int
}

// EgoGraph is the subgraph within Depth hops of the Ego node.
type EgoGraph struct {
	Ego   NodeID
	Depth int
	Nodes []Node
	Edges []Edge
}

// AddNode adds n, or, if a node with the same ID exists, keeps the existing
// node with the lower of the two hops.
func (g *EgoGraph) AddNode(n Node) {
	for i := range g.Nodes {
		if g.Nodes[i].ID == n.ID {
			g.Nodes[i].Hop = min(g.Nodes[i].Hop, n.Hop)
			return
		}
	}
	g.Nodes = append(g.Nodes, n)
}

// AddEdge adds e, treating a weight below 1 as 1. An edge with the same
// From, To and Kind is stored once, with the larger weight: the same edge
// reported from both of its ends must not count twice.
func (g *EgoGraph) AddEdge(e Edge) {
	e.Weight = max(e.Weight, 1)
	for i := range g.Edges {
		old := &g.Edges[i]
		if old.From == e.From && old.To == e.To && old.Kind == e.Kind {
			old.Weight = max(old.Weight, e.Weight)
			return
		}
	}
	g.Edges = append(g.Edges, e)
}

// Sort puts nodes in (Hop, URI, line, column, ID) order and edges in order
// of their From node's location, then their To node's location, then Kind.
// The ID breaks ties between nodes without a location.
func (g *EgoGraph) Sort() {
	sort.SliceStable(g.Nodes, func(i, j int) bool {
		a, b := g.Nodes[i], g.Nodes[j]
		if a.Hop != b.Hop {
			return a.Hop < b.Hop
		}
		if c := compareLocation(a, b); c != 0 {
			return c < 0
		}
		return a.ID < b.ID
	})
	byID := make(map[NodeID]Node, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	cmpID := func(a, b NodeID) int {
		na, okA := byID[a]
		nb, okB := byID[b]
		if okA && okB {
			if c := compareLocation(na, nb); c != 0 {
				return c
			}
		}
		return strings.Compare(string(a), string(b))
	}
	sort.SliceStable(g.Edges, func(i, j int) bool {
		a, b := g.Edges[i], g.Edges[j]
		if c := cmpID(a.From, b.From); c != 0 {
			return c < 0
		}
		if c := cmpID(a.To, b.To); c != 0 {
			return c < 0
		}
		return a.Kind.rank() < b.Kind.rank()
	})
}

// compareLocation orders nodes by URI, then by the start of their name.
func compareLocation(a, b Node) int {
	if c := strings.Compare(a.URI, b.URI); c != 0 {
		return c
	}
	if a.Selection.StartLine != b.Selection.StartLine {
		return a.Selection.StartLine - b.Selection.StartLine
	}
	return a.Selection.StartCol - b.Selection.StartCol
}

// Validate checks the graph's invariants: the ego is present with hop 0,
// every other node has a hop from 1 to Depth, node IDs are unique, and
// every edge joins two nodes of the graph with a known kind and a positive
// weight.
func (g *EgoGraph) Validate() error {
	var errs []error
	nodes := make(map[NodeID]bool, len(g.Nodes))
	for _, n := range g.Nodes {
		if nodes[n.ID] {
			errs = append(errs, fmt.Errorf("duplicate node %s", n.ID))
		}
		nodes[n.ID] = true
		switch {
		case n.ID == g.Ego && n.Hop != 0:
			errs = append(errs, fmt.Errorf("ego %s has hop %d, want 0", n.ID, n.Hop))
		case n.ID != g.Ego && (n.Hop < 1 || n.Hop > g.Depth):
			errs = append(errs, fmt.Errorf("node %s has hop %d, want 1..%d", n.ID, n.Hop, g.Depth))
		}
	}
	if !nodes[g.Ego] {
		errs = append(errs, fmt.Errorf("ego %s is not a node of the graph", g.Ego))
	}
	for _, e := range g.Edges {
		if !nodes[e.From] || !nodes[e.To] {
			errs = append(errs, fmt.Errorf("edge %s -> %s joins a node outside the graph", e.From, e.To))
		}
		if e.Kind.rank() < 0 {
			errs = append(errs, fmt.Errorf("edge %s -> %s has unknown kind %q", e.From, e.To, e.Kind))
		}
		if e.Weight < 1 {
			errs = append(errs, fmt.Errorf("edge %s -> %s has weight %d, want at least 1", e.From, e.To, e.Weight))
		}
	}
	return errors.Join(errs...)
}
