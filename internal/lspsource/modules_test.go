// Acceptance tests for module nodes and contains/imports edges, against
// the lspfake sample scenario (shapes.go in example.com/sample, and
// report/report.go in example.com/sample/report).
package lspsource_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
	"github.com/Duara-Cortex/ego-dir/internal/lspsource"
)

var (
	contains = []graph.EdgeKind{graph.EdgeContains}
	allKinds = []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates,
		graph.EdgeImports, graph.EdgeContains, graph.EdgeReferences}
)

func TestModuleEgo(t *testing.T) {
	// The cursor on "report" in "package report" (line 2, column 9).
	f := setup(t)
	g := f.buildIn(t, lspfake.SampleReportFile, 2, 9, graph.Options{Depth: 1, Kinds: defaultKinds})
	ego := g.Nodes[0]
	want := graph.Node{
		ID: "module:example.com/sample/report", Name: "report", Qualified: "example.com/sample/report",
		Kind: graph.KindModule, URI: lsp.FileURI(filepath.Join(f.root, "report")),
		Signature: "package report", Doc: "Package report describes the sample's shapes.",
	}
	if !reflect.DeepEqual(ego, want) {
		t.Errorf("ego =\n %+v\nwant\n %+v", ego, want)
	}
	// Anywhere on the name counts, including just after it.
	for _, col := range []int{9, 12, 15} {
		g := f.buildIn(t, lspfake.SampleReportFile, 2, col, graph.Options{Depth: 1, Kinds: contains})
		if g.Ego != want.ID {
			t.Errorf("column %d: ego %s, want the module", col, g.Ego)
		}
	}
}

func TestModuleImports(t *testing.T) {
	f := setup(t)
	// Default edge kinds include imports.
	g := f.buildIn(t, lspfake.SampleReportFile, 2, 9, graph.Options{Depth: 1, Kinds: defaultKinds})
	nodes, edges := summary(g)
	wantNodes := []string{
		"module:example.com/sample/report hop0",
		"module:fmt hop1", "module:strings hop1", // no location: sorted first, by ID
		"module:example.com/sample hop1",
	}
	if !reflect.DeepEqual(nodes, wantNodes) {
		t.Errorf("nodes = %v, want %v", nodes, wantNodes)
	}
	wantEdges := []string{
		"module:example.com/sample/report -imports-> module:example.com/sample x1",
		"module:example.com/sample/report -imports-> module:fmt x1",
		"module:example.com/sample/report -imports-> module:strings x1",
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %v, want %v", edges, wantEdges)
	}
	byID := map[graph.NodeID]graph.Node{}
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}
	// The workspace's own package points at its directory and has hover;
	// the standard library is known by its path alone.
	sample := byID["module:example.com/sample"]
	if sample.URI != lsp.FileURI(f.root) || sample.Name != "sample" || sample.Signature != "package sample" || sample.Doc != "" {
		t.Errorf("sample module = %+v", sample)
	}
	fmtNode := byID["module:fmt"]
	if fmtNode != (graph.Node{ID: "module:fmt", Name: "fmt", Qualified: "fmt", Kind: graph.KindModule, Hop: 1}) {
		t.Errorf("fmt module = %+v, want no location, signature or doc", fmtNode)
	}
}

func TestModuleImportsAreOutgoingOnly(t *testing.T) {
	f := setup(t)
	// Who imports a module is not derived: upstream of sample is empty,
	// although report imports it.
	g := f.buildIn(t, lspfake.SampleFile, 1, 9, graph.Options{Depth: 2, Kinds: allKinds, Direction: graph.DirIn})
	if nodes, edges := summary(g); len(nodes) != 1 || len(edges) != 0 {
		t.Errorf("graph = %v %v, want only the ego", nodes, edges)
	}
	// Symbols import nothing: imports leave modules only.
	g = f.build(t, 18, 6, graph.Options{Depth: 2, Kinds: []graph.EdgeKind{graph.EdgeImports}})
	if nodes, edges := summary(g); len(nodes) != 1 || len(edges) != 0 {
		t.Errorf("graph = %v %v, want only the ego", nodes, edges)
	}
}

func TestModuleMembers(t *testing.T) {
	f := setup(t)
	tests := []struct {
		rel       string
		line, col int
		want      []string
	}{
		// Methods belong to their type, not to the package.
		{lspfake.SampleFile, 1, 9, []string{
			"module:example.com/sample -contains-> NewRect@15 x1",
			"module:example.com/sample -contains-> Rect@9 x1",
			"module:example.com/sample -contains-> Report@27 x1",
			"module:example.com/sample -contains-> Shape@4 x1",
			"module:example.com/sample -contains-> TotalArea@18 x1",
		}},
		{lspfake.SampleReportFile, 2, 9, []string{
			"module:example.com/sample/report -contains-> Describe@12 x1",
			"module:example.com/sample/report -contains-> Label@17 x1",
		}},
	}
	for _, tc := range tests {
		g := f.buildIn(t, tc.rel, tc.line, tc.col, graph.Options{Depth: 1, Kinds: contains})
		if _, edges := summary(g); !reflect.DeepEqual(edges, tc.want) {
			t.Errorf("%s: edges = %v, want %v", tc.rel, edges, tc.want)
		}
	}
}

func TestTypeMembers(t *testing.T) {
	f := setup(t)
	tests := []struct {
		name      string
		line, col int
		want      []string
	}{
		// Fields are nested in the outline; the method is declared outside
		// the struct and named "(Rect).Area".
		{"struct", 9, 6, []string{"Rect@9 -contains-> Area@12 x1", "Rect@9 -contains-> H@9 x1", "Rect@9 -contains-> W@9 x1"}},
		{"interface", 4, 6, []string{"Shape@4 -contains-> Area@5 x1"}},
		{"function", 18, 6, nil},
		{"field", 9, 19, nil},
	}
	for _, tc := range tests {
		g := f.build(t, tc.line, tc.col, graph.Options{Depth: 1, Kinds: contains, Direction: graph.DirOut})
		if _, edges := summary(g); !reflect.DeepEqual(edges, tc.want) {
			t.Errorf("%s: edges = %v, want %v", tc.name, edges, tc.want)
		}
	}
}

func TestParents(t *testing.T) {
	f := setup(t)
	tests := []struct {
		name      string
		rel       string
		line, col int
		want      string
	}{
		{"method, by its receiver", lspfake.SampleFile, 12, 15, "Rect@9 -contains-> Area@12 x1"},
		{"field, by the outline", lspfake.SampleFile, 9, 22, "Rect@9 -contains-> H@9 x1"},
		{"interface method, by the outline", lspfake.SampleFile, 5, 2, "Shape@4 -contains-> Area@5 x1"},
		{"top-level function", lspfake.SampleFile, 18, 6, "module:example.com/sample -contains-> TotalArea@18 x1"},
		{"top-level type", lspfake.SampleFile, 9, 6, "module:example.com/sample -contains-> Rect@9 x1"},
		{"function in another package", lspfake.SampleReportFile, 12, 6, "module:example.com/sample/report -contains-> Describe@12 x1"},
	}
	for _, tc := range tests {
		g := f.buildIn(t, tc.rel, tc.line, tc.col, graph.Options{Depth: 1, Kinds: contains, Direction: graph.DirIn})
		if _, edges := summary(g); !reflect.DeepEqual(edges, []string{tc.want}) {
			t.Errorf("%s: edges = %v, want [%s]", tc.name, edges, tc.want)
		}
	}
	// Two hops up from a method: its type, then the package.
	g := f.build(t, 12, 15, graph.Options{Depth: 2, Kinds: contains, Direction: graph.DirIn})
	nodes, _ := summary(g)
	if want := []string{"Area@12 hop0", "Rect@9 hop1", "module:example.com/sample hop2"}; !reflect.DeepEqual(nodes, want) {
		t.Errorf("nodes = %v, want %v", nodes, want)
	}
}

func TestFieldNodes(t *testing.T) {
	f := setup(t)
	g := f.build(t, 9, 6, graph.Options{Depth: 1, Kinds: contains, Direction: graph.DirOut})
	for _, n := range g.Nodes {
		if n.Name != "W" {
			continue
		}
		// gopls hovers "field W float64 // size=8, offset=0".
		want := graph.Node{ID: n.ID, Name: "W", Qualified: "Rect.W", Kind: graph.KindField, URI: f.uri,
			Range:     graph.Range{StartLine: 9, StartCol: 19, EndLine: 9, EndCol: 31},
			Selection: graph.Range{StartLine: 9, StartCol: 19, EndLine: 9, EndCol: 20},
			Signature: "field W float64", Hop: 1}
		if !reflect.DeepEqual(n, want) {
			t.Errorf("W =\n %+v\nwant\n %+v", n, want)
		}
		return
	}
	t.Error("no node for the field W")
}

func TestImportsThenMembers(t *testing.T) {
	// report imports sample; at depth 2 the members of sample follow.
	f := setup(t)
	g := f.buildIn(t, lspfake.SampleReportFile, 2, 9, graph.Options{Depth: 2, Kinds: []graph.EdgeKind{graph.EdgeImports, graph.EdgeContains}})
	nodes, _ := summary(g)
	want := []string{
		"module:example.com/sample/report hop0",
		"module:fmt hop1", "module:strings hop1", "module:example.com/sample hop1",
		"Describe@12 hop1", "Label@17 hop1",
		"Shape@4 hop2", "Rect@9 hop2", "NewRect@15 hop2", "TotalArea@18 hop2", "Report@27 hop2",
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Errorf("nodes = %v\nwant %v", nodes, want)
	}
}

func TestModuleNodesNeverReachTheServer(t *testing.T) {
	// Every kind, both ways, three hops: from a module and from a symbol.
	// No request may name a module's directory, or no document at all.
	for _, ego := range []struct {
		rel       string
		line, col int
	}{{lspfake.SampleReportFile, 2, 9}, {lspfake.SampleFile, 1, 9}, {lspfake.SampleReportFile, 12, 6}, {lspfake.SampleFile, 12, 15}} {
		f := setup(t)
		g := f.buildIn(t, ego.rel, ego.line, ego.col, graph.Options{Depth: 3, Kinds: allKinds})
		if len(g.Nodes) < 2 {
			t.Errorf("%s:%d: graph too small to test: %v", ego.rel, ego.line, g.Nodes)
		}
		for _, m := range f.server.Received() {
			var params struct {
				TextDocument *struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Item *struct {
					URI string `json:"uri"`
				} `json:"item"`
			}
			_ = json.Unmarshal(m.Params, &params)
			var uri string
			switch {
			case params.TextDocument != nil:
				uri = params.TextDocument.URI
			case params.Item != nil:
				uri = params.Item.URI
			default:
				continue
			}
			if !strings.HasSuffix(uri, ".go") {
				t.Errorf("%s:%d: %s about %q, not a Go file", ego.rel, ego.line, m.Method, uri)
			}
		}
	}
}

func TestModulesOutsideTheRoot(t *testing.T) {
	// With the root at report/, the sample package is outside the
	// workspace: it is kept, known by its path alone, and has no members.
	f := setup(t)
	src := lspsource.New(f.client, filepath.Join(f.root, "report"))
	ctx := context.Background()
	g, err := graph.Build(ctx, src, f.fileURI(lspfake.SampleReportFile), 1, 8,
		graph.Options{Depth: 2, Kinds: []graph.EdgeKind{graph.EdgeImports, graph.EdgeContains}, Direction: graph.DirOut})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if n.ID != "module:example.com/sample" {
			continue
		}
		if n != (graph.Node{ID: n.ID, Name: "sample", Qualified: "example.com/sample", Kind: graph.KindModule, Hop: 1}) {
			t.Errorf("sample = %+v, want a node without location or hover", n)
		}
		_, edges := summary(g)
		for _, e := range edges {
			if strings.HasPrefix(e, "module:example.com/sample -") {
				t.Errorf("edge %s leaves a module outside the root", e)
			}
		}
		return
	}
	t.Error("no node for the imported sample package")
}

func TestNoOutlineMeansNoContains(t *testing.T) {
	// A server without documentSymbol: no members, no parents, no error.
	f := setupWith(t, func(s *lspfake.Server, _ string) {
		s.Handle("textDocument/documentSymbol", func(context.Context, json.RawMessage) (any, error) {
			return nil, &lspfake.Error{Code: lspfake.CodeMethodNotFound, Message: "unsupported"}
		})
	})
	for _, at := range [][2]int{{9, 6}, {12, 15}} {
		g := f.build(t, at[0], at[1], graph.Options{Depth: 2, Kinds: contains})
		if nodes, edges := summary(g); len(nodes) != 1 || len(edges) != 0 {
			t.Errorf("%v: graph = %v %v, want only the ego", at, nodes, edges)
		}
	}
	// The module's own edges do not need the outline for imports.
	g := f.buildIn(t, lspfake.SampleReportFile, 2, 9, graph.Options{Depth: 1, Kinds: allKinds})
	if _, edges := summary(g); len(edges) != 3 {
		t.Errorf("edges = %v, want the three imports", edges)
	}
}

func TestNoHoverForAModule(t *testing.T) {
	f := setupWith(t, func(s *lspfake.Server, _ string) {
		s.Handle("textDocument/hover", func(context.Context, json.RawMessage) (any, error) {
			return nil, &lspfake.Error{Code: lspfake.CodeMethodNotFound, Message: "unsupported"}
		})
	})
	g := f.buildIn(t, lspfake.SampleReportFile, 2, 9, graph.Options{Depth: 1, Kinds: defaultKinds})
	if ego := g.Nodes[0]; ego.Signature != "" || ego.Doc != "" || ego.Name != "report" {
		t.Errorf("ego = %+v, want a module without signature or doc", ego)
	}
}
