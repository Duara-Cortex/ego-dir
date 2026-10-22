// Package lspsource implements graph.SymbolSource on top of a language
// server, mapping LSP answers onto ego-graph nodes and edges.
//
// Edge derivation:
//   - calls: call hierarchy (incoming/outgoing); weight = call sites.
//   - implements: textDocument/implementation, oriented concrete ->
//     interface using the document outline.
//   - instantiates: textDocument/typeDefinition of the symbol.
//   - references: textDocument/references, grouped by enclosing symbol.
//   - contains: the document outline, from a module to its top-level
//     symbols and from a type to its members. Members a language declares
//     outside their type (Go methods) are matched by the Language's Owner.
//   - imports: from a module to the modules it imports, read by the
//     Language (Go: go/parser), with weight = importing files.
//
// What differs between programming languages (outline names, hover
// decorations, modules) comes from a Language, chosen by file extension.
//
// A module node has no position. The ego is a module when the position is
// on the module's name in a file's module declaration ("package report").
// Only contains (out) and imports (out) edges leave a module; no request
// is sent to the server about one other than hover on that name.
//
// An error the server returns for a neighbour request (it does not support
// it, or it does not apply to the symbol) counts as no neighbours.
// Neighbours declared outside the workspace root (the standard library, the
// module cache) are dropped: their paths depend on the machine. A module
// outside the root is kept, identified by its path alone.
package lspsource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// Client is the part of *lsp.Client the source uses.
type Client interface {
	DidOpen(ctx context.Context, uri, languageID, text string) error
	Definition(ctx context.Context, uri string, pos lsp.Position) ([]lsp.Location, error)
	TypeDefinition(ctx context.Context, uri string, pos lsp.Position) ([]lsp.Location, error)
	Implementation(ctx context.Context, uri string, pos lsp.Position) ([]lsp.Location, error)
	References(ctx context.Context, uri string, pos lsp.Position, includeDeclaration bool) ([]lsp.Location, error)
	Hover(ctx context.Context, uri string, pos lsp.Position) (string, error)
	DocumentSymbols(ctx context.Context, uri string) ([]lsp.DocumentSymbol, error)
	PrepareCallHierarchy(ctx context.Context, uri string, pos lsp.Position) ([]lsp.CallHierarchyItem, error)
	IncomingCalls(ctx context.Context, item lsp.CallHierarchyItem) ([]lsp.CallHierarchyIncomingCall, error)
	OutgoingCalls(ctx context.Context, item lsp.CallHierarchyItem) ([]lsp.CallHierarchyOutgoingCall, error)
}

// Source is a graph.SymbolSource backed by a language server. It caches
// files, outlines and nodes; it is safe for concurrent use.
type Source struct {
	c     Client
	root  string
	roots []string // root, and root with symlinks resolved; empty: no limit

	mu      sync.Mutex
	files   map[string]*file         // by URI
	loading map[string]chan struct{} // closed when the URI's load ends
	nodes   map[graph.NodeID]*info   // every node handed out
}

var _ graph.SymbolSource = (*Source)(nil)

// New returns a source that asks c about the workspace at root.
func New(c Client, root string) *Source {
	s := &Source{c: c, root: root, files: map[string]*file{}, loading: map[string]chan struct{}{}, nodes: map[graph.NodeID]*info{}}
	if root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			s.roots = append(s.roots, abs)
			if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
				s.roots = append(s.roots, real)
			}
		}
	}
	return s
}

// inWorkspace reports whether uri is a file under the workspace root.
func (s *Source) inWorkspace(uri string) bool {
	if len(s.roots) == 0 {
		return true
	}
	path, err := lsp.URIToPath(uri)
	if err != nil {
		return false
	}
	for _, root := range s.roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// file is a document read from disk and its outline.
type file struct {
	uri     string
	path    string
	lang    Language
	lines   []string
	symbols []symbol // flattened outline
}

// symbol is one outline entry with its parent's kind, name and name range.
type symbol struct {
	lsp.DocumentSymbol
	parentKind lsp.SymbolKind
	parentName string
	parentSel  *lsp.Range // nil at the top level
}

// info is what the source remembers about a node it returned.
type info struct {
	node     graph.Node
	sel      lsp.Range // the name, in LSP coordinates
	item     *lsp.CallHierarchyItem
	prepared bool // call hierarchy was asked for item
	abstract bool // an interface or interface method
	module   *Module
	lang     Language // the module's language
}

// Resolve returns the symbol at pos: the callee when pos is a call site,
// otherwise the declaration that pos refers to.
func (s *Source) Resolve(ctx context.Context, uri string, line, char int) (graph.Node, error) {
	f, err := s.load(ctx, uri)
	if err != nil {
		return graph.Node{}, err
	}
	pos := lsp.Position{Line: line, Character: char}
	if m, ok := f.moduleHeaderAt(pos); ok {
		inf, err := s.moduleNode(ctx, m, f.lang)
		if err != nil {
			return graph.Node{}, err
		}
		return inf.node, nil
	}
	items, err := s.c.PrepareCallHierarchy(ctx, uri, pos)
	if err := hardError(err); err != nil {
		return graph.Node{}, err
	}
	if len(items) > 0 {
		inf, err := s.fromItem(ctx, items[0])
		return inf.node, err
	}
	locs, err := s.c.Definition(ctx, uri, pos)
	if err := hardError(err); err != nil {
		return graph.Node{}, err
	}
	if len(locs) == 0 {
		return graph.Node{}, fmt.Errorf("no symbol at %s:%d:%d", uri, line+1, char+1)
	}
	inf, err := s.fromLocation(ctx, locs[0])
	return inf.node, err
}

// Neighbors implements graph.SymbolSource.
func (s *Source) Neighbors(ctx context.Context, n graph.Node, dir graph.Direction, kinds []graph.EdgeKind) ([]graph.Neighbor, error) {
	s.mu.Lock()
	inf := s.nodes[n.ID]
	s.mu.Unlock()
	if inf == nil {
		return nil, fmt.Errorf("unknown node %s", n.ID)
	}
	if inf.module != nil {
		return s.moduleNeighbors(ctx, inf, dir, kinds)
	}
	var out []graph.Neighbor
	for _, kind := range kinds {
		var nbrs []graph.Neighbor
		var err error
		switch kind {
		case graph.EdgeContains:
			if dir == graph.DirIn {
				nbrs, err = s.parent(ctx, inf)
			} else {
				nbrs, err = s.members(ctx, inf)
			}
		case graph.EdgeCalls:
			nbrs, err = s.calls(ctx, inf, dir)
		case graph.EdgeImplements:
			nbrs, err = s.implements(ctx, inf, dir)
		case graph.EdgeInstantiates:
			if dir == graph.DirOut {
				nbrs, err = s.instantiates(ctx, inf)
			}
		case graph.EdgeReferences:
			if dir == graph.DirIn {
				nbrs, err = s.references(ctx, inf)
			}
		}
		if err != nil {
			return nil, err
		}
		out = append(out, nbrs...)
	}
	return out, nil
}

func (s *Source) calls(ctx context.Context, inf *info, dir graph.Direction) ([]graph.Neighbor, error) {
	item, err := s.callItem(ctx, inf)
	if err != nil || item == nil {
		return nil, err
	}
	var out []graph.Neighbor
	if dir == graph.DirIn {
		calls, err := s.c.IncomingCalls(ctx, *item)
		if err := hardError(err); err != nil {
			return nil, err
		}
		for _, c := range calls {
			if !s.inWorkspace(c.From.URI) {
				continue
			}
			nb, err := s.fromItem(ctx, c.From)
			if err != nil {
				return nil, err
			}
			out = append(out, graph.Neighbor{Node: nb.node, Kind: graph.EdgeCalls, Weight: len(c.FromRanges)})
		}
		return out, nil
	}
	calls, err := s.c.OutgoingCalls(ctx, *item)
	if err := hardError(err); err != nil {
		return nil, err
	}
	for _, c := range calls {
		if !s.inWorkspace(c.To.URI) {
			continue
		}
		nb, err := s.fromItem(ctx, c.To)
		if err != nil {
			return nil, err
		}
		out = append(out, graph.Neighbor{Node: nb.node, Kind: graph.EdgeCalls, Weight: len(c.FromRanges)})
	}
	return out, nil
}

// callItem returns the node's call hierarchy item, preparing it once.
func (s *Source) callItem(ctx context.Context, inf *info) (*lsp.CallHierarchyItem, error) {
	s.mu.Lock()
	item, prepared := inf.item, inf.prepared
	s.mu.Unlock()
	if item != nil || prepared {
		return item, nil
	}
	items, err := s.c.PrepareCallHierarchy(ctx, inf.node.URI, inf.sel.Start)
	if err := hardError(err); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	inf.prepared = true
	if len(items) > 0 {
		inf.item = &items[0]
	}
	return inf.item, nil
}

// implements returns the other side of implements edges. They point from
// concrete to abstract, so an abstract node has them incoming and a
// concrete node outgoing.
func (s *Source) implements(ctx context.Context, inf *info, dir graph.Direction) ([]graph.Neighbor, error) {
	if inf.abstract != (dir == graph.DirIn) {
		return nil, nil
	}
	return s.located(ctx, inf, graph.EdgeImplements, s.c.Implementation)
}

func (s *Source) instantiates(ctx context.Context, inf *info) ([]graph.Neighbor, error) {
	return s.located(ctx, inf, graph.EdgeInstantiates, s.c.TypeDefinition)
}

// located turns a location request about inf into neighbours, skipping inf
// itself (the type definition of a type is the type).
func (s *Source) located(ctx context.Context, inf *info, kind graph.EdgeKind,
	request func(context.Context, string, lsp.Position) ([]lsp.Location, error)) ([]graph.Neighbor, error) {
	locs, err := request(ctx, inf.node.URI, inf.sel.Start)
	if err := hardError(err); err != nil {
		return nil, err
	}
	var out []graph.Neighbor
	for _, loc := range locs {
		if !s.inWorkspace(loc.URI) {
			continue
		}
		nb, err := s.fromLocation(ctx, loc)
		if err != nil {
			return nil, err
		}
		if nb.node.ID != inf.node.ID {
			out = append(out, graph.Neighbor{Node: nb.node, Kind: kind, Weight: 1})
		}
	}
	return out, nil
}

// references groups the uses of inf by the symbol that contains them.
func (s *Source) references(ctx context.Context, inf *info) ([]graph.Neighbor, error) {
	locs, err := s.c.References(ctx, inf.node.URI, inf.sel.Start, false)
	if err := hardError(err); err != nil {
		return nil, err
	}
	var order []graph.NodeID
	byID := map[graph.NodeID]*graph.Neighbor{}
	for _, loc := range locs {
		if !s.inWorkspace(loc.URI) {
			continue
		}
		f, err := s.load(ctx, loc.URI)
		if err != nil {
			return nil, err
		}
		sym := f.enclosing(loc.Range.Start)
		if sym == nil {
			continue // e.g. a package-level var initialiser
		}
		nb, err := s.fromSymbol(ctx, f, *sym, nil)
		if err != nil {
			return nil, err
		}
		if r := byID[nb.node.ID]; r != nil {
			r.Weight++
			continue
		}
		byID[nb.node.ID] = &graph.Neighbor{Node: nb.node, Kind: graph.EdgeReferences, Weight: 1}
		order = append(order, nb.node.ID)
	}
	out := make([]graph.Neighbor, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// fromItem returns the node for a call hierarchy item.
func (s *Source) fromItem(ctx context.Context, item lsp.CallHierarchyItem) (*info, error) {
	f, err := s.load(ctx, item.URI)
	if err != nil {
		return nil, err
	}
	sym := f.named(item.SelectionRange.Start)
	if sym == nil {
		sym = &symbol{DocumentSymbol: lsp.DocumentSymbol{
			Name: item.Name, Kind: item.Kind, Range: item.Range, SelectionRange: item.SelectionRange,
		}}
	}
	return s.fromSymbol(ctx, f, *sym, &item)
}

// fromLocation returns the node whose name is at loc.
func (s *Source) fromLocation(ctx context.Context, loc lsp.Location) (*info, error) {
	f, err := s.load(ctx, loc.URI)
	if err != nil {
		return nil, err
	}
	sym := f.named(loc.Range.Start)
	if sym == nil {
		sym = &symbol{DocumentSymbol: lsp.DocumentSymbol{
			Name: f.text(loc.Range), Kind: lsp.SymbolKindVariable, Range: loc.Range, SelectionRange: loc.Range,
		}}
	}
	return s.fromSymbol(ctx, f, *sym, nil)
}

// fromSymbol builds (once) the node for an outline symbol.
func (s *Source) fromSymbol(ctx context.Context, f *file, sym symbol, item *lsp.CallHierarchyItem) (*info, error) {
	sel := f.toGraph(sym.SelectionRange)
	id := graph.NewNodeID(f.uri, sel)
	s.mu.Lock()
	if inf := s.nodes[id]; inf != nil {
		if inf.item == nil && item != nil {
			inf.item = item
		}
		s.mu.Unlock()
		return inf, nil
	}
	s.mu.Unlock()

	name, qualified := f.lang.SplitName(sym.Name)
	if qualified == "" && sym.parentName != "" {
		qualified = sym.parentName + "." + name
	}
	hover, err := s.c.Hover(ctx, f.uri, sym.SelectionRange.Start)
	if err := hardError(err); err != nil {
		return nil, err
	}
	signature, doc := f.lang.ParseHover(hover)
	if signature == "" {
		signature = declarationLine(f.text(sym.Range))
	}
	inf := &info{
		node: graph.Node{
			ID:        id,
			Name:      name,
			Qualified: qualified,
			Kind:      kindOf(sym.Kind),
			URI:       f.uri,
			Range:     f.toGraph(sym.Range),
			Selection: sel,
			Signature: signature,
			Doc:       doc,
			Body:      f.text(sym.Range),
		},
		sel:      sym.SelectionRange,
		item:     item,
		abstract: sym.Kind == lsp.SymbolKindInterface || sym.parentKind == lsp.SymbolKindInterface,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev := s.nodes[id]; prev != nil { // built concurrently
		return prev, nil
	}
	s.nodes[id] = inf
	return inf, nil
}

// load reads, opens and outlines a document once. Concurrent loads of one
// URI wait for the first; if it fails, the next one tries again.
func (s *Source) load(ctx context.Context, uri string) (*file, error) {
	for {
		s.mu.Lock()
		if f := s.files[uri]; f != nil {
			s.mu.Unlock()
			return f, nil
		}
		wait := s.loading[uri]
		if wait == nil {
			done := make(chan struct{})
			s.loading[uri] = done
			s.mu.Unlock()
			f, err := s.open(ctx, uri)
			s.mu.Lock()
			delete(s.loading, uri)
			if err == nil {
				s.files[uri] = f
			}
			close(done)
			s.mu.Unlock()
			return f, err
		}
		s.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// open reads a document, opens it on the server and outlines it.
func (s *Source) open(ctx context.Context, uri string) (*file, error) {
	path, err := lsp.URIToPath(uri)
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(src)
	if err := s.c.DidOpen(ctx, uri, lsp.LanguageID(path), text); err != nil {
		return nil, err
	}
	outline, err := s.c.DocumentSymbols(ctx, uri)
	if err := hardError(err); err != nil {
		return nil, err
	}
	f := &file{uri: uri, path: path, lang: languageOf(uri), lines: strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")}
	f.flatten(outline, nil)
	return f, nil
}

func (f *file) flatten(syms []lsp.DocumentSymbol, parent *lsp.DocumentSymbol) {
	for _, sym := range syms {
		entry := symbol{DocumentSymbol: sym}
		entry.Children = nil
		entry.Range.Start = f.lang.DeclarationStart(f.line(entry.Range.Start.Line), entry.Range.Start)
		if parent != nil {
			entry.parentKind = parent.Kind
			entry.parentName, _ = f.lang.SplitName(parent.Name)
			sel := parent.SelectionRange
			entry.parentSel = &sel
		}
		f.symbols = append(f.symbols, entry)
		f.flatten(sym.Children, &sym)
	}
}

// named returns the innermost symbol whose name contains pos.
func (f *file) named(pos lsp.Position) *symbol {
	return f.innermost(pos, func(s symbol) lsp.Range { return s.SelectionRange })
}

// enclosing returns the innermost symbol whose declaration contains pos.
func (f *file) enclosing(pos lsp.Position) *symbol {
	return f.innermost(pos, func(s symbol) lsp.Range { return s.Range })
}

func (f *file) innermost(pos lsp.Position, span func(symbol) lsp.Range) *symbol {
	var best *symbol
	for i := range f.symbols {
		r := span(f.symbols[i])
		if !contains(r, pos) {
			continue
		}
		if best == nil || contains(span(*best), r.Start) && contains(span(*best), r.End) {
			best = &f.symbols[i]
		}
	}
	return best
}

func contains(r lsp.Range, p lsp.Position) bool {
	after := p.Line > r.Start.Line || (p.Line == r.Start.Line && p.Character >= r.Start.Character)
	before := p.Line < r.End.Line || (p.Line == r.End.Line && p.Character <= r.End.Character)
	return after && before
}

func (f *file) line(i int) string {
	if i < 0 || i >= len(f.lines) {
		return ""
	}
	return f.lines[i]
}

// toGraph converts an LSP range to 1-based editor coordinates.
func (f *file) toGraph(r lsp.Range) graph.Range {
	sl, sc := lsp.PositionToEditor(f.line(r.Start.Line), r.Start)
	el, ec := lsp.PositionToEditor(f.line(r.End.Line), r.End)
	return graph.Range{StartLine: sl, StartCol: sc, EndLine: el, EndCol: ec}
}

// text returns the source covered by r.
func (f *file) text(r lsp.Range) string {
	g := f.toGraph(r)
	var b strings.Builder
	for l := g.StartLine; l <= g.EndLine; l++ {
		runes := []rune(f.line(l - 1))
		from, to := 0, len(runes)
		if l == g.StartLine {
			from = min(g.StartCol-1, len(runes))
		}
		if l == g.EndLine {
			to = min(g.EndCol-1, len(runes))
		}
		if l > g.StartLine {
			b.WriteByte('\n')
		}
		if from < to {
			b.WriteString(string(runes[from:to]))
		}
	}
	return b.String()
}

// hardError drops errors that mean "no answer" (an error response from
// the server) and keeps the ones that mean the session failed.
func hardError(err error) error {
	var rpcErr *lsp.RPCError
	if errors.As(err, &rpcErr) {
		return nil
	}
	return err
}

// declarationLine is the fallback signature: the first line of the
// declaration, without an opening brace.
func declarationLine(decl string) string {
	first, _, _ := strings.Cut(decl, "\n")
	first = strings.TrimSpace(first)
	if i := strings.Index(first, " {"); i >= 0 {
		first = first[:i]
	}
	return strings.TrimSuffix(first, "{")
}

// kindOf maps protocol symbol kinds to graph kinds.
func kindOf(k lsp.SymbolKind) graph.SymbolKind {
	switch k {
	case lsp.SymbolKindFunction, lsp.SymbolKindConstructor:
		return graph.KindFunction
	case lsp.SymbolKindMethod:
		return graph.KindMethod
	case lsp.SymbolKindStruct:
		return graph.KindStruct
	case lsp.SymbolKindClass:
		return graph.KindClass
	case lsp.SymbolKindInterface:
		return graph.KindInterface
	case lsp.SymbolKindEnum, lsp.SymbolKindTypeParameter:
		return graph.KindType
	case lsp.SymbolKindVariable:
		return graph.KindVariable
	case lsp.SymbolKindConstant, lsp.SymbolKindEnumMember:
		return graph.KindConstant
	case lsp.SymbolKindField, lsp.SymbolKindProperty:
		return graph.KindField
	case lsp.SymbolKindModule, lsp.SymbolKindNamespace, lsp.SymbolKindPackage:
		return graph.KindModule
	default:
		return graph.KindOther
	}
}
