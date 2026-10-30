// Acceptance tests for Python: the real lsp.Client and graph.Build against
// the lspfake Python scenario, which answers like pyright 1.1.414.
package lspsource_test

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
	"github.com/Duara-Cortex/ego-dir/internal/lspsource"
)

func setupPython(t *testing.T) fixture { return setupPythonWith(t, "", nil) }

// setupPythonWith starts the Python scenario, changed by customise, with
// the source's root set to sourceRoot ("" means the workspace root; "-"
// means none).
func setupPythonWith(t *testing.T, sourceRoot string, customise func(s *lspfake.Server, root string)) fixture {
	t.Helper()
	root, err := lspfake.PythonRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := lspfake.New()
	lspfake.Python(s)
	if customise != nil {
		customise(s, root)
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
	switch sourceRoot {
	case "":
		sourceRoot = root
	case "-":
		sourceRoot = ""
	}
	return fixture{
		closeServer: func() { serverEnd.Close() },
		server:      s,
		client:      c,
		src:         lspsource.New(c, sourceRoot),
		root:        root,
		uri:         lsp.FileURI(filepath.Join(root, lspfake.PythonFile)),
	}
}

func (f fixture) py(t *testing.T, line, col int, opts graph.Options) *graph.EgoGraph {
	t.Helper()
	return f.buildIn(t, lspfake.PythonFile, line, col, opts)
}

func edgesOf(g *graph.EgoGraph) []string {
	_, edges := summary(g)
	return edges
}

func TestPythonEgoNode(t *testing.T) {
	f := setupPython(t)
	ego := f.py(t, 31, 5, graph.Options{Depth: 1, Kinds: defaultKinds, Body: graph.BodyEgo}).Nodes[0]
	want := graph.Node{
		ID:        graph.NewNodeID(f.uri, graph.Range{StartLine: 31, StartCol: 5, EndLine: 31, EndCol: 15}),
		Name:      "total_area",
		Kind:      graph.KindFunction,
		URI:       f.uri,
		Range:     graph.Range{StartLine: 31, StartCol: 1, EndLine: 36, EndCol: 17},
		Selection: graph.Range{StartLine: 31, StartCol: 5, EndLine: 31, EndCol: 15},
		Signature: "def total_area(shapes: list[Shape]) -> float",
		Doc:       "Sum the area of every shape.",
		Body: "def total_area(shapes: list[Shape]) -> float:\n    \"\"\"Sum the area of every shape.\"\"\"\n    total = 0.0\n" +
			"    for s in shapes:\n        total += s.area()\n    return total",
	}
	if !reflect.DeepEqual(ego, want) {
		t.Errorf("ego =\n %+v\nwant\n %+v", ego, want)
	}
}

func TestPythonMethodNode(t *testing.T) {
	f := setupPython(t)
	g := f.py(t, 21, 9, graph.Options{Depth: 1, Kinds: defaultKinds, Body: graph.BodyEgo})
	ego := g.Nodes[0]
	// Qualified from the class it is nested in; the body without the
	// class's indentation; the range from "def" to its last line.
	if ego.Name != "area" || ego.Qualified != "Rect.area" || ego.Kind != graph.KindMethod ||
		ego.Signature != "def area(self: Self@Rect) -> float" || ego.Doc != "Return the rectangle's area." ||
		ego.Range != (graph.Range{StartLine: 21, StartCol: 5, EndLine: 23, EndCol: 31}) ||
		ego.Body != "def area(self) -> float:\n    \"\"\"Return the rectangle's area.\"\"\"\n    return self.w * self.h" {
		t.Errorf("Rect.area = %+v", ego)
	}
	// pyright resolves s.area() to Shape.area, so nothing calls Rect.area.
	if edges := edgesOf(g); len(edges) != 0 {
		t.Errorf("Rect.area edges = %v, want none", edges)
	}
	// A decorated method starts at its decorator.
	if shapeArea := f.py(t, 10, 9, graph.Options{Depth: 1, Kinds: defaultKinds}).Nodes[0]; shapeArea.Range.StartLine != 9 ||
		shapeArea.Qualified != "Shape.area" {
		t.Errorf("Shape.area = %+v, want it to start at @abstractmethod on line 9", shapeArea)
	}
}

func TestPythonCalls(t *testing.T) {
	f := setupPython(t)
	g := f.py(t, 31, 5, graph.Options{Depth: 2, Kinds: defaultKinds})
	nodes, edges := summary(g)
	wantNodes := []string{"total_area@31 hop0", "area@10 hop1", "report@39 hop1"}
	wantEdges := []string{"report@39 -calls-> total_area@31 x1", "total_area@31 -calls-> area@10 x1"}
	if !reflect.DeepEqual(nodes, wantNodes) || !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("total_area depth 2:\nnodes %v\nwant  %v\nedges %v\nwant  %v", nodes, wantNodes, edges, wantEdges)
	}
}

func TestPythonCallWeightsCountDistinctSites(t *testing.T) {
	f := setupPython(t)
	// pyright lists the one call site of s.area() twice among the callers
	// of Shape.area; the weight is still one, as from the caller's side.
	if edges := edgesOf(f.py(t, 10, 9, graph.Options{Depth: 1, Kinds: defaultKinds, Direction: graph.DirIn})); !reflect.DeepEqual(edges,
		[]string{"total_area@31 -calls-> area@10 x1"}) {
		t.Errorf("callers of Shape.area = %v", edges)
	}
	if edges := edgesOf(f.py(t, 39, 5, graph.Options{Depth: 1, Kinds: defaultKinds, Direction: graph.DirOut})); !reflect.DeepEqual(edges,
		[]string{"report@39 -calls-> new_rect@26 x2", "report@39 -calls-> total_area@31 x1"}) {
		t.Errorf("callees of report = %v", edges)
	}
}

func TestPythonConstructorCallsInstantiate(t *testing.T) {
	f := setupPython(t)
	want := []string{"new_rect@26 -instantiates-> Rect@14 x1"}
	if edges := edgesOf(f.py(t, 26, 5, graph.Options{Depth: 1, Kinds: defaultKinds, Direction: graph.DirOut})); !reflect.DeepEqual(edges, want) {
		t.Errorf("new_rect, out = %v, want %v", edges, want)
	}
	if edges := edgesOf(f.py(t, 14, 7, graph.Options{Depth: 1, Kinds: defaultKinds, Direction: graph.DirIn})); !reflect.DeepEqual(edges, want) {
		t.Errorf("Rect, in = %v, want %v", edges, want)
	}
	// Not a call as well, and not when only calls are followed.
	calls := []graph.EdgeKind{graph.EdgeCalls}
	if edges := edgesOf(f.py(t, 26, 5, graph.Options{Depth: 1, Kinds: calls, Direction: graph.DirOut})); len(edges) != 0 {
		t.Errorf("new_rect, out, calls only = %v, want none", edges)
	}
	if edges := edgesOf(f.py(t, 14, 7, graph.Options{Depth: 1, Kinds: calls, Direction: graph.DirBoth})); len(edges) != 0 {
		t.Errorf("Rect, calls only = %v, want none", edges)
	}
}

func TestPythonHasNoImplements(t *testing.T) {
	f := setupPython(t)
	implements := []graph.EdgeKind{graph.EdgeImplements}
	for _, at := range [][2]int{{14, 7}, {6, 7}, {21, 9}, {10, 9}} { // Rect, Shape, Rect.area, Shape.area
		g := f.py(t, at[0], at[1], graph.Options{Depth: 2, Kinds: implements})
		if len(g.Nodes) != 1 || len(g.Edges) != 0 {
			t.Errorf("implements at %v = %v, want the ego alone", at, edgesOf(g))
		}
	}
	// It was asked, and pyright refused it as an unhandled method.
	if len(f.server.Requests("textDocument/implementation")) == 0 {
		t.Error("implementation was never asked")
	}
}

func TestPythonMembers(t *testing.T) {
	f := setupPython(t)
	contains := []graph.EdgeKind{graph.EdgeContains}
	// A class holds its methods and attributes; parameters are not members.
	g := f.py(t, 14, 7, graph.Options{Depth: 1, Kinds: contains, Direction: graph.DirOut})
	nodes, _ := summary(g)
	if want := []string{"Rect@14 hop0", "__init__@17 hop1", "w@18 hop1", "h@19 hop1", "area@21 hop1"}; !reflect.DeepEqual(nodes, want) {
		t.Errorf("members of Rect = %v, want %v", nodes, want)
	}
	for _, n := range g.Nodes[1:] {
		if n.Name == "w" && (n.Kind != graph.KindField || n.Qualified != "Rect.w" || n.Signature != "w: float") {
			t.Errorf("attribute w = %+v, want the field Rect.w", n)
		}
	}
	// A function's parameters and locals are not its members.
	if edges := edgesOf(f.py(t, 31, 5, graph.Options{Depth: 1, Kinds: contains, Direction: graph.DirOut})); len(edges) != 0 {
		t.Errorf("members of total_area = %v, want none", edges)
	}
	// Upstream: the class, then the module.
	if edges := edgesOf(f.py(t, 21, 9, graph.Options{Depth: 2, Kinds: contains, Direction: graph.DirIn})); !reflect.DeepEqual(edges,
		[]string{"Rect@14 -contains-> area@21 x1", "module:shapes -contains-> Rect@14 x1"}) {
		t.Errorf("parents of Rect.area = %v", edges)
	}
}

func TestPythonModule(t *testing.T) {
	f := setupPython(t)
	all := []graph.EdgeKind{graph.EdgeImports, graph.EdgeContains}
	g := f.buildIn(t, lspfake.PythonReportFile, 1, 1, graph.Options{Depth: 1, Kinds: all, Direction: graph.DirOut})
	ego := g.Nodes[0]
	if ego.ID != "module:report.report" || ego.Name != "report" || ego.Qualified != "report.report" || ego.Kind != graph.KindModule ||
		ego.URI != f.fileURI(lspfake.PythonReportFile) || ego.Signature != "module report.report" || ego.Doc != "Format shapes for people." {
		t.Errorf("module ego = %+v", ego)
	}
	nodes, edges := summary(g)
	// Sorted by file: a module outside the workspace has none.
	wantNodes := []string{"module:report.report hop0", "module:string hop1", "describe@8 hop1", "label@13 hop1", "module:shapes hop1"}
	wantEdges := []string{
		"module:report.report -contains-> describe@8 x1", "module:report.report -contains-> label@13 x1",
		"module:report.report -imports-> module:shapes x1", "module:report.report -imports-> module:string x1",
	}
	if !reflect.DeepEqual(nodes, wantNodes) || !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("report.report:\nnodes %v\nwant  %v\nedges %v\nwant  %v", nodes, wantNodes, edges, wantEdges)
	}
	for _, n := range g.Nodes {
		switch n.ID {
		case "module:shapes": // in the workspace: its file and its docstring
			if n.URI != f.uri || n.Doc != "Shapes and their areas." {
				t.Errorf("shapes = %+v", n)
			}
		case "module:string": // outside: its path alone
			if n.URI != "" || n.Signature != "" || n.Doc != "" {
				t.Errorf("string = %+v", n)
			}
		}
	}
	// Anywhere on the docstring's first line is the module; the next lines are not.
	if g := f.buildIn(t, lspfake.PythonReportFile, 1, 20, graph.Options{Depth: 1, Kinds: all}); g.Ego != "module:report.report" {
		t.Errorf("ego at 1:20 = %s", g.Ego)
	}
	if _, err := f.src.Resolve(context.Background(), f.fileURI(lspfake.PythonReportFile), 2, 0); err == nil {
		t.Error("the line after the docstring resolved to something")
	}
	// The package's __init__.py is the module report.
	if g := f.buildIn(t, lspfake.PythonPackageFile, 1, 1, graph.Options{Depth: 1, Kinds: all}); g.Ego != "module:report" || len(g.Edges) != 0 {
		t.Errorf("report/__init__.py = %v %v", g.Ego, edgesOf(g))
	}
	// Hover is not asked about a module (pyright would answer null).
	for _, h := range f.server.Requests("textDocument/hover") {
		var p struct {
			Position lsp.Position `json:"position"`
		}
		if err := json.Unmarshal(h.Params, &p); err != nil || p.Position.Line == 0 {
			t.Errorf("hover asked on a docstring's line: %s", h.Params)
		}
	}
}

// TestPythonReferencesFromOtherFiles is the reason for Companions: pyright
// finds uses only in open files until it has analysed the workspace, so
// the source opens the files that import a module before asking.
func TestPythonReferencesFromOtherFiles(t *testing.T) {
	refs := []graph.EdgeKind{graph.EdgeReferences}
	opts := graph.Options{Depth: 1, Kinds: refs, Direction: graph.DirIn}
	// 25:36 and 27:11 are in new_rect; 8:17 is in describe; the import at
	// 5:20 is in no symbol.
	want := []string{"describe@8 -references-> Rect@14 x1", "new_rect@26 -references-> Rect@14 x2"}
	if edges := edgesOf(setupPython(t).py(t, 14, 7, opts)); !reflect.DeepEqual(edges, want) {
		t.Errorf("references to Rect = %v, want %v", edges, want)
	}
	// Without a root there is no workspace to search: report.py stays
	// closed and pyright does not see describe.
	if edges := edgesOf(setupPythonWith(t, "-", nil).py(t, 14, 7, opts)); !reflect.DeepEqual(edges, want[1:]) {
		t.Errorf("references to Rect without companions = %v, want %v", edges, want[1:])
	}
	// The attribute's use in report.py is found the same way.
	if edges := edgesOf(setupPython(t).py(t, 18, 14, opts)); !reflect.DeepEqual(edges,
		[]string{"area@21 -references-> w@18 x1", "describe@8 -references-> w@18 x1"}) {
		t.Errorf("references to Rect.w = %v", edges)
	}
}

func TestPythonCompanionsOpenOnce(t *testing.T) {
	f := setupPython(t)
	opts := graph.Options{Depth: 2, Kinds: []graph.EdgeKind{graph.EdgeReferences, graph.EdgeCalls}, Direction: graph.DirIn}
	f.py(t, 14, 7, opts)
	f.py(t, 18, 14, opts)
	opened := map[string]int{}
	for _, m := range f.server.Requests("textDocument/didOpen") {
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(m.Params, &p)
		opened[p.TextDocument.URI]++
	}
	want := map[string]int{f.uri: 1, f.fileURI(lspfake.PythonReportFile): 1}
	if !reflect.DeepEqual(opened, want) {
		t.Errorf("didOpen counts = %v, want %v", opened, want)
	}
}

func TestPythonOutsideTheWorkspaceIsDropped(t *testing.T) {
	f := setupPythonWith(t, "", func(s *lspfake.Server, root string) {
		venv := lsp.FileURI(filepath.Join(root, ".venv", "lib", "python3.12", "site-packages", "vendor", "use.py"))
		s.Handle("textDocument/references", func(context.Context, json.RawMessage) (any, error) {
			return []lsp.Location{{URI: venv, Range: lsp.Range{Start: lsp.Position{Line: 3}, End: lsp.Position{Line: 3, Character: 4}}}}, nil
		})
	})
	all := []graph.EdgeKind{graph.EdgeCalls, graph.EdgeInstantiates, graph.EdgeImports, graph.EdgeContains, graph.EdgeReferences}
	g := f.buildIn(t, lspfake.PythonReportFile, 13, 5, graph.Options{Depth: 2, Kinds: all})
	for _, n := range g.Nodes {
		if n.URI != "" && n.URI != f.uri && n.URI != f.fileURI(lspfake.PythonReportFile) {
			t.Errorf("%s is outside the workspace: %s", n.Name, n.URI)
		}
	}
	// label calls string.capwords, in pyright's stubs: dropped. The use
	// in a virtual environment under the root: dropped.
	if edges := edgesOf(g); !reflect.DeepEqual(edges, []string{"module:report.report -contains-> label@13 x1"}) {
		t.Errorf("label = %v, want only its module", edges)
	}
}

func TestPythonIsDeterministic(t *testing.T) {
	all := []graph.EdgeKind{graph.EdgeCalls, graph.EdgeImplements, graph.EdgeInstantiates, graph.EdgeImports, graph.EdgeContains, graph.EdgeReferences}
	opts := graph.Options{Depth: 3, Kinds: all, Body: graph.BodyAll}
	first := setupPython(t).py(t, 14, 7, opts)
	for i := 0; i < 3; i++ {
		if again := setupPython(t).py(t, 14, 7, opts); !reflect.DeepEqual(again, first) {
			t.Fatalf("build %d differs", i+2)
		}
	}
}

// TestCallHierarchyAskedOncePerNode: calls and instantiates come from the
// same answers, so following both asks each node's callers and callees
// once.
func TestCallHierarchyAskedOncePerNode(t *testing.T) {
	opts := graph.Options{Depth: 3, Kinds: defaultKinds, Direction: graph.DirBoth}
	for name, f := range map[string]fixture{"go": setup(t), "python": setupPython(t)} {
		if name == "go" {
			f.build(t, 18, 6, opts)
		} else {
			f.py(t, 39, 5, opts)
		}
		for _, method := range []string{"callHierarchy/incomingCalls", "callHierarchy/outgoingCalls"} {
			seen := map[string]int{}
			for _, m := range f.server.Requests(method) {
				var p struct {
					Item struct {
						URI            string    `json:"uri"`
						SelectionRange lsp.Range `json:"selectionRange"`
					} `json:"item"`
				}
				_ = json.Unmarshal(m.Params, &p)
				key := p.Item.URI + "#" + itoa(p.Item.SelectionRange.Start.Line) + ":" + itoa(p.Item.SelectionRange.Start.Character)
				if seen[key]++; seen[key] > 1 {
					t.Errorf("%s: %s asked %d times for %s", name, method, seen[key], key)
				}
			}
			if len(seen) == 0 {
				t.Errorf("%s: %s never asked", name, method)
			}
		}
	}
}
