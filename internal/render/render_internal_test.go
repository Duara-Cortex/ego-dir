package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rasJacobCharles/ego-dir/internal/graph"
)

func TestInlineCode(t *testing.T) {
	tests := map[string]string{
		"Area":           "`Area`",
		"x `json:\"a\"`": "`` x `json:\"a\"` ``",
		"`start":         "`` `start ``",
	}
	for in, want := range tests {
		if got := inlineCode(in); got != want {
			t.Errorf("inlineCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFence(t *testing.T) {
	if got := fence("go", "a\nb", "  "); got != "  ```go\n  a\n  b\n  ```" {
		t.Errorf("fence = %q", got)
	}
	// Code containing a fence gets a longer one.
	if got := fence("", "x ``` y", ""); got != "````\nx ``` y\n````" {
		t.Errorf("fence with backticks = %q", got)
	}
}

func TestFenceLanguage(t *testing.T) {
	tests := map[string]string{
		"file:///a.go": "go", "file:///a.PY": "python", "file:///a.rs": "rust",
		"file:///a.ts": "typescript", "file:///a.tsx": "typescript",
		"file:///a.js": "javascript", "file:///a.jsx": "javascript",
		"file:///a.php": "php", "file:///a.txt": "",
	}
	for in, want := range tests {
		if got := fenceLanguage(in); got != want {
			t.Errorf("fenceLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCollapse(t *testing.T) {
	if got := collapse("type Rect struct {\n\tW, H float64\n}"); got != "type Rect struct { W, H float64 }" {
		t.Errorf("collapse = %q", got)
	}
}

// star: ego E; U calls E; E calls D; I is implemented by E; E instantiates T;
// C is a cycle partner (E calls C, C calls E); X is joined only to D's caller
// side (X calls D), so it is on neither side of E.
func star() *graph.EgoGraph {
	mk := func(name string, line int, kind graph.SymbolKind) graph.Node {
		sel := graph.Range{StartLine: line, StartCol: 6, EndLine: line, EndCol: 6 + len(name)}
		return graph.Node{ID: graph.NewNodeID("file:///ws/pkg/a.go", sel), Name: name, Kind: kind,
			URI: "file:///ws/pkg/a.go", Range: graph.Range{StartLine: line, StartCol: 1, EndLine: line, EndCol: 9}, Selection: sel, Hop: 1}
	}
	e, u, d := mk("E", 10, graph.KindFunction), mk("U", 20, graph.KindFunction), mk("D", 30, graph.KindFunction)
	i, ty, c, x := mk("I", 40, graph.KindInterface), mk("T", 50, graph.KindStruct), mk("C", 60, graph.KindFunction), mk("X", 70, graph.KindFunction)
	e.Hop = 0
	g := &graph.EgoGraph{Ego: e.ID, Depth: 2}
	for _, n := range []graph.Node{e, u, d, i, ty, c, x} {
		g.AddNode(n)
	}
	for _, edge := range []graph.Edge{
		{From: u.ID, To: e.ID, Kind: graph.EdgeCalls},
		{From: e.ID, To: d.ID, Kind: graph.EdgeCalls},
		{From: e.ID, To: i.ID, Kind: graph.EdgeImplements},
		{From: e.ID, To: ty.ID, Kind: graph.EdgeInstantiates},
		{From: e.ID, To: c.ID, Kind: graph.EdgeCalls},
		{From: c.ID, To: e.ID, Kind: graph.EdgeCalls},
		{From: x.ID, To: d.ID, Kind: graph.EdgeCalls},
	} {
		g.AddEdge(edge)
	}
	return g
}

func TestRoles(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, star(), "/ws"); err != nil {
		t.Fatal(err)
	}
	var out jsonGraph
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, n := range out.Nodes {
		roles[n.Name] = n.Role
	}
	want := map[string]string{
		"E": roleEgo, "U": roleUpstream, "D": roleDownstream, "T": roleDownstream,
		"I": roleBoth, // implements is walked both ways
		"C": roleBoth, "X": roleRelated,
	}
	for name, role := range want {
		if roles[name] != role {
			t.Errorf("role of %s = %q, want %q", name, roles[name], role)
		}
	}
	if out.Nodes[0].File != "pkg/a.go" || out.Ego != "pkg/a.go:10:6" {
		t.Errorf("paths not relative to root: %+v", out.Nodes[0])
	}
}

func TestImplementationsAndTypeDependencies(t *testing.T) {
	var buf bytes.Buffer
	if err := Markdown(&buf, star(), "/ws"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	impl := section(out, "## Implementations")
	if !strings.Contains(impl, "- `I` (interface) pkg/a.go:40") || strings.Contains(impl, "`T`") {
		t.Errorf("Implementations section:\n%s", impl)
	}
	deps := section(out, "## Type dependencies")
	if !strings.Contains(deps, "- `T` (struct) pkg/a.go:50") || strings.Contains(deps, "`I`") {
		t.Errorf("Type dependencies section:\n%s", deps)
	}
	if strings.Contains(section(out, "## Upstream (callers)"), "`X`") || strings.Contains(section(out, "## Downstream (callees)"), "`X`") {
		t.Error("X is on neither side of the ego but was listed as an alter")
	}
	if !strings.Contains(section(out, "## Edges"), "| `X` pkg/a.go:70 | calls | `D` pkg/a.go:30 | 1 |") {
		t.Errorf("X's edge missing from the table:\n%s", section(out, "## Edges"))
	}
}

// section returns the text between heading and the next "## " heading.
func section(md, heading string) string {
	i := strings.Index(md, heading+"\n")
	if i < 0 {
		return ""
	}
	rest := md[i+len(heading)+1:]
	if j := strings.Index(rest, "\n## "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestPathsOutsideRootAndOtherSchemes(t *testing.T) {
	v := &view{root: "/ws", byID: map[graph.NodeID]graph.Node{}}
	if got := v.file(graph.Node{URI: "file:///other/b.go"}); got != "../other/b.go" {
		t.Errorf("file outside root = %q", got)
	}
	if got := v.file(graph.Node{URI: "jdt://contents/x.class"}); got != "jdt://contents/x.class" {
		t.Errorf("non-file URI = %q", got)
	}
	noRoot := &view{byID: map[graph.NodeID]graph.Node{}}
	if got := noRoot.file(graph.Node{URI: "file:///ws/a.go"}); got != "/ws/a.go" {
		t.Errorf("no root = %q", got)
	}
	if got := v.id("unknown#L1:1"); got != "unknown#L1:1" {
		t.Errorf("id of unknown node = %q", got)
	}
	if got := v.label("unknown#L1:1"); got != "`unknown#L1:1`" {
		t.Errorf("label of unknown node = %q", got)
	}
}

func TestPipeInNameIsEscapedInTable(t *testing.T) {
	g := star()
	for i := range g.Nodes {
		if g.Nodes[i].Name == "U" {
			g.Nodes[i].Name = "op|"
		}
	}
	var buf bytes.Buffer
	if err := Markdown(&buf, g, "/ws"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "| `op\\|` pkg/a.go:20 | calls |") {
		t.Errorf("pipe not escaped:\n%s", section(buf.String(), "## Edges"))
	}
}
