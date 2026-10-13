// Acceptance tests for rendering module nodes and contains edges.
package render_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
	"github.com/Duara-Cortex/ego-dir/internal/render"
)

const reportURI = "file:///workspace/sample/report/report.go"

// reportModule is the graph of package report (the ego) at depth 1 with
// imports and contains: it imports the workspace's sample package and fmt,
// and contains Describe.
func reportModule() *graph.EgoGraph {
	ego := graph.Node{ID: graph.NewModuleID("example.com/sample/report"), Name: "report", Qualified: "example.com/sample/report",
		Kind: graph.KindModule, URI: "file:///workspace/sample/report", Signature: "package report",
		Doc: "Package report describes the sample's shapes."}
	sample := graph.Node{ID: graph.NewModuleID("example.com/sample"), Name: "sample", Qualified: "example.com/sample",
		Kind: graph.KindModule, URI: "file:///workspace/sample", Signature: "package sample", Hop: 1}
	fmtNode := graph.Node{ID: graph.NewModuleID("fmt"), Name: "fmt", Qualified: "fmt", Kind: graph.KindModule, Hop: 1}
	describe := graph.Node{ID: graph.NewNodeID(reportURI, sel(12, 6, 8)), Name: "Describe", Kind: graph.KindFunction,
		URI: reportURI, Range: graph.Range{StartLine: 12, StartCol: 1, EndLine: 14, EndCol: 2}, Selection: sel(12, 6, 8),
		Signature: "func Describe(r sample.Rect) string", Hop: 1}
	g := &graph.EgoGraph{Ego: ego.ID, Depth: 1}
	for _, n := range []graph.Node{describe, fmtNode, ego, sample} {
		g.AddNode(n)
	}
	g.AddEdge(graph.Edge{From: ego.ID, To: sample.ID, Kind: graph.EdgeImports, Weight: 1})
	g.AddEdge(graph.Edge{From: ego.ID, To: fmtNode.ID, Kind: graph.EdgeImports, Weight: 2})
	g.AddEdge(graph.Edge{From: ego.ID, To: describe.ID, Kind: graph.EdgeContains, Weight: 1})
	g.Sort()
	return g
}

func TestModuleMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := render.Markdown(&buf, reportModule(), root); err != nil {
		t.Fatal(err)
	}
	md := buf.String()
	for _, want := range []string{
		"# Ego: `report`\n\n- kind: module\n- location: example.com/sample/report\n- depth: 1\n",
		"## Signature\n\n```\npackage report\n```\n",
		"## Members\n\n- `Describe` (function) report/report.go:12-14\n  - signature: `func Describe(r sample.Rect) string`\n\n",
		"## Type dependencies\n\n- `fmt` (module) fmt\n- `sample` (module) example.com/sample\n  - signature: `package sample`\n\n",
		"| `report` example.com/sample/report | imports | `fmt` fmt | 2 |\n",
		"| `report` example.com/sample/report | imports | `sample` example.com/sample | 1 |\n",
		"| `report` example.com/sample/report | contains | `Describe` report/report.go:12 | 1 |\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown lacks %q\n--- got ---\n%s", want, md)
		}
	}
	if strings.Contains(md, "/workspace") {
		t.Errorf("Markdown has an absolute path:\n%s", md)
	}
}

func TestModuleJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := render.JSON(&buf, reportModule(), root); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Ego   string           `json:"ego"`
		Nodes []map[string]any `json:"nodes"`
		Edges []map[string]any `json:"edges"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Ego != "module:example.com/sample/report" {
		t.Errorf("ego = %q", out.Ego)
	}
	byID := map[string]map[string]any{}
	for _, n := range out.Nodes {
		byID[n["id"].(string)] = n
	}
	// In the workspace: the directory, relative to the root ("." for the
	// root itself), and no range.
	tests := map[string]map[string]any{
		"module:example.com/sample/report": {"id": "module:example.com/sample/report", "name": "report", "qualified": "example.com/sample/report",
			"kind": "module", "file": "report", "hop": 0.0, "role": "ego", "signature": "package report",
			"doc": "Package report describes the sample's shapes."},
		"module:example.com/sample": {"id": "module:example.com/sample", "name": "sample", "qualified": "example.com/sample",
			"kind": "module", "file": ".", "hop": 1.0, "role": "downstream", "signature": "package sample"},
		// Outside the workspace: no file either.
		"module:fmt": {"id": "module:fmt", "name": "fmt", "qualified": "fmt", "kind": "module", "hop": 1.0, "role": "downstream"},
	}
	for id, want := range tests {
		if got := byID[id]; !reflect.DeepEqual(got, want) {
			t.Errorf("node %s =\n %v\nwant\n %v", id, got, want)
		}
	}
	if d := byID["report/report.go:12:6"]; d == nil || d["range"] == nil || d["file"] != "report/report.go" {
		t.Errorf("Describe = %v, want a file and a range", d)
	}
	wantEdge := map[string]any{"from": "module:example.com/sample/report", "to": "module:fmt", "kind": "imports", "weight": 2.0}
	found := false
	for _, e := range out.Edges {
		found = found || reflect.DeepEqual(e, wantEdge)
	}
	if !found {
		t.Errorf("edges = %v, want %v", out.Edges, wantEdge)
	}
}

func TestMembersListsTheParentFirst(t *testing.T) {
	uri := "file:///workspace/sample/shapes.go"
	mk := func(name string, kind graph.SymbolKind, s graph.Range, hop int) graph.Node {
		return graph.Node{ID: graph.NewNodeID(uri, s), Name: name, Kind: kind, URI: uri, Range: s, Selection: s, Hop: hop}
	}
	rect := mk("Rect", graph.KindStruct, sel(9, 6, 4), 0)
	w := mk("W", graph.KindField, sel(9, 19, 1), 1)
	module := graph.Node{ID: graph.NewModuleID("example.com/sample"), Name: "sample", Qualified: "example.com/sample",
		Kind: graph.KindModule, URI: "file:///workspace/sample", Hop: 1}
	g := &graph.EgoGraph{Ego: rect.ID, Depth: 1}
	for _, n := range []graph.Node{w, rect, module} {
		g.AddNode(n)
	}
	g.AddEdge(graph.Edge{From: rect.ID, To: w.ID, Kind: graph.EdgeContains})
	g.AddEdge(graph.Edge{From: module.ID, To: rect.ID, Kind: graph.EdgeContains})
	var buf bytes.Buffer
	if err := render.Markdown(&buf, g, root); err != nil {
		t.Fatal(err)
	}
	want := "## Members\n\n- parent: `sample` (module) example.com/sample\n- `W` (field) shapes.go:9\n\n"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("Markdown lacks %q\n--- got ---\n%s", want, buf.String())
	}
}

func TestModuleDefinition(t *testing.T) {
	n := reportModule().Nodes[0]
	var buf bytes.Buffer
	if err := render.Definition(&buf, n, root); err != nil {
		t.Fatal(err)
	}
	if want := "- kind: module\n- location: module:example.com/sample/report\n- declaration: example.com/sample/report\n"; !strings.Contains(buf.String(), want) {
		t.Errorf("Definition lacks %q\n--- got ---\n%s", want, buf.String())
	}
	buf.Reset()
	if err := render.DefinitionJSON(&buf, n, root); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Symbol map[string]any `json:"symbol"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Symbol["id"] != "module:example.com/sample/report" || out.Symbol["file"] != "report" || out.Symbol["range"] != nil {
		t.Errorf("symbol = %v, want the module id, its directory and no range", out.Symbol)
	}
}
