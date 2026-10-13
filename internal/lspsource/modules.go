package lspsource

import (
	"context"
	"path/filepath"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// moduleHeaderAt returns the file's module when pos is on the module's
// name in the file's module declaration. A module that cannot be read
// counts as no module here: the position is then an ordinary one.
func (f *file) moduleHeaderAt(pos lsp.Position) (Module, bool) {
	m, ok, err := f.lang.ModuleOf(f.path)
	if err != nil || !ok {
		return Module{}, false
	}
	for _, mf := range m.Files {
		if filepath.Clean(mf.Path) == filepath.Clean(f.path) && contains(mf.Header, pos) {
			return m, true
		}
	}
	return Module{}, false
}

// moduleNode builds (once) the node of module m. A module in the
// workspace points at its directory and takes its signature and doc from
// hover on its name in its first file. Any other module is known by its
// path alone and has no members or imports.
func (s *Source) moduleNode(ctx context.Context, m Module, lang Language) (*info, error) {
	id := graph.NewModuleID(m.Path)
	s.mu.Lock()
	if inf := s.nodes[id]; inf != nil {
		s.mu.Unlock()
		return inf, nil
	}
	s.mu.Unlock()

	node := graph.Node{ID: id, Name: m.Name, Qualified: m.Path, Kind: graph.KindModule}
	if m.Dir != "" && len(m.Files) > 0 && s.inWorkspace(lsp.FileURI(m.Dir)) {
		node.URI = lsp.FileURI(m.Dir)
		first := m.Files[0]
		uri := lsp.FileURI(first.Path)
		if _, err := s.load(ctx, uri); err != nil {
			return nil, err
		}
		hover, err := s.c.Hover(ctx, uri, first.Header.Start)
		if err := hardError(err); err != nil {
			return nil, err
		}
		node.Signature, node.Doc = lang.ParseHover(hover)
	} else {
		m.Dir, m.Files = "", nil
	}
	inf := &info{node: node, module: &m, lang: lang}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev := s.nodes[id]; prev != nil { // built concurrently
		return prev, nil
	}
	s.nodes[id] = inf
	return inf, nil
}

// moduleNeighbors answers for a module node: its members and the modules
// it imports, both outgoing. Nothing is derived upstream of a module.
func (s *Source) moduleNeighbors(ctx context.Context, inf *info, dir graph.Direction, kinds []graph.EdgeKind) ([]graph.Neighbor, error) {
	if dir != graph.DirOut {
		return nil, nil
	}
	var out []graph.Neighbor
	for _, kind := range kinds {
		var nbrs []graph.Neighbor
		var err error
		switch kind {
		case graph.EdgeContains:
			nbrs, err = s.moduleMembers(ctx, inf.module)
		case graph.EdgeImports:
			nbrs, err = s.imports(ctx, inf)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, nbrs...)
	}
	return out, nil
}

// moduleMembers returns the top-level symbols of m's files in the
// workspace, except those that belong to a type (Go methods).
func (s *Source) moduleMembers(ctx context.Context, m *Module) ([]graph.Neighbor, error) {
	var out []graph.Neighbor
	err := s.eachFile(ctx, m, func(f *file) error {
		for _, sym := range f.symbols {
			if sym.parentSel != nil || f.lang.Owner(sym.Name) != "" {
				continue
			}
			nb, err := s.fromSymbol(ctx, f, sym, nil)
			if err != nil {
				return err
			}
			out = append(out, graph.Neighbor{Node: nb.node, Kind: graph.EdgeContains, Weight: 1})
		}
		return nil
	})
	return out, err
}

func (s *Source) imports(ctx context.Context, inf *info) ([]graph.Neighbor, error) {
	imports, err := inf.lang.Imports(*inf.module)
	if err != nil {
		return nil, err
	}
	var out []graph.Neighbor
	for _, imp := range imports {
		nb, err := s.moduleNode(ctx, imp.Module, inf.lang)
		if err != nil {
			return nil, err
		}
		out = append(out, graph.Neighbor{Node: nb.node, Kind: graph.EdgeImports, Weight: imp.Weight})
	}
	return out, nil
}

// parent returns what contains the symbol: its parent in the outline, else
// the type its language says owns it, else its module.
func (s *Source) parent(ctx context.Context, inf *info) ([]graph.Neighbor, error) {
	f, sym, err := s.outlineEntry(ctx, inf)
	if err != nil || sym == nil {
		return nil, err
	}
	var p *info
	switch owner := f.lang.Owner(sym.Name); {
	case sym.parentSel != nil:
		if ps := f.named(sym.parentSel.Start); ps != nil {
			p, err = s.fromSymbol(ctx, f, *ps, nil)
		}
	case owner != "":
		var of *file
		var osym *symbol
		if of, osym, err = s.findOwner(ctx, f, owner); err == nil && osym != nil {
			p, err = s.fromSymbol(ctx, of, *osym, nil)
		}
	default:
		if m, ok, merr := f.lang.ModuleOf(f.path); merr != nil {
			err = merr
		} else if ok {
			p, err = s.moduleNode(ctx, m, f.lang)
		}
	}
	if err != nil || p == nil {
		return nil, err
	}
	return []graph.Neighbor{{Node: p.node, Kind: graph.EdgeContains, Weight: 1}}, nil
}

// members returns the symbol's children in the outline and, for a
// top-level type, the members its language declares outside it.
func (s *Source) members(ctx context.Context, inf *info) ([]graph.Neighbor, error) {
	f, sym, err := s.outlineEntry(ctx, inf)
	if err != nil || sym == nil {
		return nil, err
	}
	var out []graph.Neighbor
	add := func(f *file, child symbol) error {
		nb, err := s.fromSymbol(ctx, f, child, nil)
		if err == nil {
			out = append(out, graph.Neighbor{Node: nb.node, Kind: graph.EdgeContains, Weight: 1})
		}
		return err
	}
	for _, child := range f.symbols {
		if child.parentSel != nil && *child.parentSel == sym.SelectionRange {
			if err := add(f, child); err != nil {
				return nil, err
			}
		}
	}
	if sym.parentSel != nil {
		return out, nil
	}
	m, ok, err := f.lang.ModuleOf(f.path)
	if err != nil || !ok {
		return out, err
	}
	err = s.eachFile(ctx, &m, func(mf *file) error {
		for _, member := range mf.symbols {
			if member.parentSel == nil && mf.lang.Owner(member.Name) == sym.Name {
				if err := add(mf, member); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return out, err
}

// findOwner returns the top-level symbol named owner in f or, failing
// that, in the other files of f's module.
func (s *Source) findOwner(ctx context.Context, f *file, owner string) (*file, *symbol, error) {
	if sym := f.topLevel(owner); sym != nil {
		return f, sym, nil
	}
	m, ok, err := f.lang.ModuleOf(f.path)
	if err != nil || !ok {
		return nil, nil, err
	}
	var of *file
	var found *symbol
	err = s.eachFile(ctx, &m, func(mf *file) error {
		if found == nil {
			if sym := mf.topLevel(owner); sym != nil {
				of, found = mf, sym
			}
		}
		return nil
	})
	return of, found, err
}

// eachFile calls fn for each of m's files in the workspace, in order.
func (s *Source) eachFile(ctx context.Context, m *Module, fn func(*file) error) error {
	for _, mf := range m.Files {
		uri := lsp.FileURI(mf.Path)
		if !s.inWorkspace(uri) {
			continue
		}
		f, err := s.load(ctx, uri)
		if err != nil {
			return err
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

// outlineEntry returns the node's file and its entry in the outline, or a
// nil entry when the node is not in the outline.
func (s *Source) outlineEntry(ctx context.Context, inf *info) (*file, *symbol, error) {
	f, err := s.load(ctx, inf.node.URI)
	if err != nil {
		return nil, nil, err
	}
	sym := f.named(inf.sel.Start)
	if sym == nil || sym.SelectionRange != inf.sel {
		return f, nil, nil
	}
	return f, sym, nil
}

// topLevel returns the top-level symbol with the outline name name.
func (f *file) topLevel(name string) *symbol {
	for i := range f.symbols {
		if f.symbols[i].parentSel == nil && f.symbols[i].Name == name {
			return &f.symbols[i]
		}
	}
	return nil
}
