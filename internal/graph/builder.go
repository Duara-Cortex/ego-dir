package graph

import (
	"context"
	"errors"
	"fmt"
)

// MaxDepth is the largest hop radius Build accepts.
const MaxDepth = 5

// Direction selects which side of the ego to explore.
type Direction int

// Directions. The zero value explores both sides.
const (
	DirBoth Direction = iota // upstream and downstream
	DirIn                    // upstream: callers, referrers
	DirOut                   // downstream: callees, dependencies
)

// BodyMode selects which nodes keep their source body.
type BodyMode int

// Body modes. The zero value keeps only the ego's body.
const (
	BodyEgo BodyMode = iota
	BodyAll
	BodyNone
)

// Neighbor is a node joined to another by one edge kind. Weight counts the
// call sites or references the edge stands for.
type Neighbor struct {
	Node   Node
	Kind   EdgeKind
	Weight int
}

// SymbolSource answers questions about symbols. It is implemented on top of
// a language server by package lspsource.
type SymbolSource interface {
	// Resolve returns the symbol at a zero-based LSP position (line,
	// UTF-16 character) in the document uri.
	Resolve(ctx context.Context, uri string, line, char int) (Node, error)
	// Neighbors returns n's neighbours joined by edges of the given kinds:
	// with DirIn, the nodes with an edge to n; with DirOut, the nodes n has
	// an edge to. Only DirIn and DirOut are passed.
	Neighbors(ctx context.Context, n Node, dir Direction, kinds []EdgeKind) ([]Neighbor, error)
}

// Options controls Build.
type Options struct {
	Depth     int        // hop radius, 1..MaxDepth
	Kinds     []EdgeKind // edge kinds to follow; at least one
	Direction Direction
	Body      BodyMode
}

func (o Options) validate() error {
	if o.Depth < 1 || o.Depth > MaxDepth {
		return fmt.Errorf("depth %d out of range 1..%d", o.Depth, MaxDepth)
	}
	if len(o.Kinds) == 0 {
		return errors.New("no edge kinds selected")
	}
	for _, k := range o.Kinds {
		if k.rank() < 0 {
			return fmt.Errorf("unknown edge kind %q", k)
		}
	}
	if o.Direction < DirBoth || o.Direction > DirOut {
		return fmt.Errorf("unknown direction %d", o.Direction)
	}
	if o.Body < BodyEgo || o.Body > BodyNone {
		return fmt.Errorf("unknown body mode %d", o.Body)
	}
	return nil
}

// Build constructs the ego-graph around the symbol at a zero-based LSP
// position. Upstream and downstream are separate breadth-first searches
// (siblings of the ego are not alters); implements edges are followed both
// ways in both. The result is sorted and valid.
func Build(ctx context.Context, src SymbolSource, uri string, line, char int, opts Options) (*EgoGraph, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ego, err := src.Resolve(ctx, uri, line, char)
	if err != nil {
		return nil, fmt.Errorf("resolve symbol: %w", err)
	}
	ego.ID = nodeID(ego)
	ego.Hop = 0

	b := &builder{
		src:   src,
		kinds: map[EdgeKind]bool{},
		cache: map[expansion][]Neighbor{},
		g:     &EgoGraph{Ego: ego.ID, Depth: opts.Depth},
	}
	for _, k := range opts.Kinds {
		b.kinds[k] = true
	}
	b.kindList = opts.Kinds
	b.g.AddNode(ego)

	if opts.Direction != DirOut {
		if err := b.search(ctx, ego, DirIn, opts.Depth); err != nil {
			return nil, err
		}
	}
	if opts.Direction != DirIn {
		if err := b.search(ctx, ego, DirOut, opts.Depth); err != nil {
			return nil, err
		}
	}

	for i := range b.g.Nodes {
		n := &b.g.Nodes[i]
		if opts.Body == BodyNone || (opts.Body == BodyEgo && n.ID != ego.ID) {
			n.Body = ""
		}
	}
	b.g.Sort()
	return b.g, nil
}

type expansion struct {
	id  NodeID
	dir Direction
}

type builder struct {
	src      SymbolSource
	kinds    map[EdgeKind]bool
	kindList []EdgeKind
	cache    map[expansion][]Neighbor // each node is asked once per direction
	g        *EgoGraph
}

// search runs a breadth-first search from ego in direction dir, adding
// every node within depth hops and every edge followed on the way.
func (b *builder) search(ctx context.Context, ego Node, dir Direction, depth int) error {
	seen := map[NodeID]bool{ego.ID: true}
	frontier := []Node{ego}
	for hop := 1; hop <= depth && len(frontier) > 0; hop++ {
		var next []Node
		for _, n := range frontier {
			for _, side := range []Direction{DirIn, DirOut} {
				if side != dir && !b.kinds[EdgeImplements] {
					continue
				}
				nbrs, err := b.neighbors(ctx, n, side)
				if err != nil {
					return err
				}
				for _, nb := range nbrs {
					// Only implements edges are followed against the
					// search direction.
					if !b.kinds[nb.Kind] || (side != dir && nb.Kind != EdgeImplements) {
						continue
					}
					alter := nb.Node
					alter.ID = nodeID(alter)
					alter.Hop = hop
					from, to := alter.ID, n.ID
					if side == DirOut {
						from, to = n.ID, alter.ID
					}
					b.g.AddNode(alter)
					b.g.AddEdge(Edge{From: from, To: to, Kind: nb.Kind, Weight: nb.Weight})
					if !seen[alter.ID] {
						seen[alter.ID] = true
						next = append(next, alter)
					}
				}
			}
		}
		frontier = next
	}
	return nil
}

func (b *builder) neighbors(ctx context.Context, n Node, dir Direction) ([]Neighbor, error) {
	key := expansion{n.ID, dir}
	if nbrs, ok := b.cache[key]; ok {
		return nbrs, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nbrs, err := b.src.Neighbors(ctx, n, dir, b.kindList)
	if err != nil {
		return nil, fmt.Errorf("expand %s: %w", n.Name, err)
	}
	b.cache[key] = nbrs
	return nbrs, nil
}

// nodeID returns n.ID, deriving it from the location when unset.
func nodeID(n Node) NodeID {
	if n.ID != "" {
		return n.ID
	}
	return NewNodeID(n.URI, n.Selection)
}
