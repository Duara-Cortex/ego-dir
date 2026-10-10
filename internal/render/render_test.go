// Acceptance tests for the renderers.
package render_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rasJacobCharles/ego-dir/internal/graph"
	"github.com/rasJacobCharles/ego-dir/internal/render"
)

var update = flag.Bool("update", false, "rewrite golden files with the current output")

const root = "/workspace/sample"
const uri = "file:///workspace/sample/shapes.go"

func sel(line, col, length int) graph.Range {
	return graph.Range{StartLine: line, StartCol: col, EndLine: line, EndCol: col + length}
}

// totalArea is the graph lspsource builds for TotalArea (shapes.go:18:6),
// depth 2, default edge kinds, in the sample workspace.
func totalArea() *graph.EgoGraph {
	mk := func(name string, kind graph.SymbolKind, s, r graph.Range, hop int, sig, doc string) graph.Node {
		return graph.Node{ID: graph.NewNodeID(uri, s), Name: name, Kind: kind, URI: uri, Range: r, Selection: s,
			Signature: sig, Doc: doc, Hop: hop}
	}
	ego := mk("TotalArea", graph.KindFunction, sel(18, 6, 9), graph.Range{StartLine: 18, StartCol: 1, EndLine: 24, EndCol: 2}, 0,
		"func TotalArea(shapes []Shape) float64", "TotalArea sums the area of every shape.")
	ego.Body = "func TotalArea(shapes []Shape) float64 {\n\tvar total float64\n\tfor _, s := range shapes {\n" +
		"\t\ttotal += s.Area()\n\t}\n\treturn total\n}"
	report := mk("Report", graph.KindFunction, sel(27, 6, 6), graph.Range{StartLine: 27, StartCol: 1, EndLine: 29, EndCol: 2}, 1,
		"func Report() float64", "Report returns the total area of two rectangles.")
	shapeArea := mk("Area", graph.KindMethod, sel(5, 2, 4), graph.Range{StartLine: 5, StartCol: 1, EndLine: 5, EndCol: 16}, 1,
		"Area() float64", "")
	rectArea := mk("Area", graph.KindMethod, sel(12, 15, 4), graph.Range{StartLine: 12, StartCol: 1, EndLine: 12, EndCol: 50}, 2,
		"func (r Rect) Area() float64", "Area returns the rectangle's area.")

	g := &graph.EgoGraph{Ego: ego.ID, Depth: 2}
	for _, n := range []graph.Node{ego, report, shapeArea, rectArea} {
		g.AddNode(n)
	}
	g.AddEdge(graph.Edge{From: report.ID, To: ego.ID, Kind: graph.EdgeCalls, Weight: 1})
	g.AddEdge(graph.Edge{From: ego.ID, To: shapeArea.ID, Kind: graph.EdgeCalls, Weight: 1})
	g.AddEdge(graph.Edge{From: rectArea.ID, To: shapeArea.ID, Kind: graph.EdgeImplements, Weight: 1})
	g.Sort()
	return g
}

func golden(t *testing.T, name string, got []byte) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return want
}

func TestMarkdownGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := render.Markdown(&buf, totalArea(), root); err != nil {
		t.Fatal(err)
	}
	want := golden(t, "total_area.golden.md", buf.Bytes())
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("Markdown output differs from testdata/total_area.golden.md\n--- got ---\n%s\n--- want ---\n%s", buf.Bytes(), want)
	}
}

func TestJSONGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := render.JSON(&buf, totalArea(), root); err != nil {
		t.Fatal(err)
	}
	// Compared by value: whitespace and key order are not part of the schema.
	var got, want any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.Bytes())
	}
	if err := json.Unmarshal(golden(t, "total_area.golden.json", buf.Bytes()), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("JSON output differs from testdata/total_area.golden.json\n--- got ---\n%s", buf.Bytes())
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
		t.Error("JSON output should end with a newline")
	}
}

func TestEmptySectionsAreKept(t *testing.T) {
	g := totalArea()
	// Keep only the ego.
	g.Nodes = g.Nodes[:1]
	g.Edges = nil
	var buf bytes.Buffer
	if err := render.Markdown(&buf, g, root); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, h := range []string{"## Signature", "## Doc", "## Body", "## Upstream (callers)", "## Downstream (callees)",
		"## Implementations", "## Type dependencies", "## Edges"} {
		i := strings.Index(out, h+"\n\n")
		if i < 0 {
			t.Errorf("section %q missing", h)
		}
	}
	if n := strings.Count(out, "_none_"); n != 5 {
		t.Errorf("%d _none_ placeholders, want 5 (upstream, downstream, implementations, type dependencies, edges)", n)
	}
}

func TestDeterministicOutput(t *testing.T) {
	for _, r := range []struct {
		name string
		fn   func(*bytes.Buffer, *graph.EgoGraph) error
	}{
		{"markdown", func(b *bytes.Buffer, g *graph.EgoGraph) error { return render.Markdown(b, g, root) }},
		{"json", func(b *bytes.Buffer, g *graph.EgoGraph) error { return render.JSON(b, g, root) }},
	} {
		var first bytes.Buffer
		if err := r.fn(&first, totalArea()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(first.String(), "TotalArea") {
			t.Fatalf("%s: output does not mention the ego:\n%s", r.name, first.String())
		}
		for seed := int64(1); seed <= 10; seed++ {
			g := totalArea()
			rng := rand.New(rand.NewSource(seed))
			rng.Shuffle(len(g.Nodes), func(i, j int) { g.Nodes[i], g.Nodes[j] = g.Nodes[j], g.Nodes[i] })
			rng.Shuffle(len(g.Edges), func(i, j int) { g.Edges[i], g.Edges[j] = g.Edges[j], g.Edges[i] })
			var again bytes.Buffer
			if err := r.fn(&again, g); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first.Bytes(), again.Bytes()) {
				t.Fatalf("%s: output depends on input order (seed %d)", r.name, seed)
			}
		}
		if strings.Contains(first.String(), root) || strings.Contains(first.String(), "file://") {
			t.Errorf("%s output contains an absolute path or URI", r.name)
		}
	}
}

func TestBodiesOnlyWhenPresent(t *testing.T) {
	g := totalArea()
	g.Nodes[0].Body = ""
	var buf bytes.Buffer
	if err := render.Markdown(&buf, g, root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "## Body\n\n_none_\n") {
		t.Errorf("ego without a body should render _none_ under ## Body:\n%s", buf.String())
	}

	g = totalArea()
	for i := range g.Nodes {
		if g.Nodes[i].Name == "Report" {
			g.Nodes[i].Body = "func Report() float64 {\n\treturn 0\n}"
		}
	}
	buf.Reset()
	if err := render.Markdown(&buf, g, root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "  ```go\n  func Report() float64 {\n  \treturn 0\n  }\n  ```\n") {
		t.Errorf("an alter's body should render as an indented code block under its entry:\n%s", buf.String())
	}
}
