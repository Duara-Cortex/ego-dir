package lspsource

import (
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

func TestSplitName(t *testing.T) {
	tests := map[string][2]string{
		"(Rect).Area":   {"Area", "Rect.Area"},
		"(*Rect).Scale": {"Scale", "Rect.Scale"},
		"TotalArea":     {"TotalArea", ""},
		"(weird":        {"(weird", ""},
	}
	for in, want := range tests {
		if s, q := golang.SplitName(in); s != want[0] || q != want[1] {
			t.Errorf("SplitName(%q) = %q, %q; want %q, %q", in, s, q, want[0], want[1])
		}
	}
}

func TestReceiverOwner(t *testing.T) {
	tests := map[string]string{"(Rect).Area": "Rect", "(*Rect).Scale": "Rect", "TotalArea": "", "Area": ""}
	for in, want := range tests {
		if got := golang.Owner(in); got != want {
			t.Errorf("Owner(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseHover(t *testing.T) {
	tests := []struct{ in, sig, doc string }{
		{"```go\nfunc F()\n```\n\nF does it.", "func F()", "F does it."},
		{"```go\nfunc F()\n```\n\nF does it.\n\nMore.\n\n[`pkg.F` on pkg.go.dev](https://pkg.go.dev/pkg#F)", "func F()", "F does it.\n\nMore."},
		{"plain text only", "", "plain text only"},
		{"```go\nunterminated", "", "```go\nunterminated"},
		{"", "", ""},
		// gopls v0.23, verbatim: rules around the doc, then a link.
		{"```go\nfunc TotalArea(shapes []Shape) float64\n```\n\n---\n\nTotalArea sums the area of every shape.\n\n\n---\n\n" +
			"[`sample.TotalArea` on pkg.go.dev](https://pkg.go.dev/example.com/sample#TotalArea)",
			"func TotalArea(shapes []Shape) float64", "TotalArea sums the area of every shape."},
		// No doc at all.
		{"```go\nfunc (Shape) Area() float64\n```\n\n---\n\n" +
			"[`(sample.Shape).Area` on pkg.go.dev](https://pkg.go.dev/example.com/sample#Shape.Area)",
			"func (Shape) Area() float64", ""},
		// A type: the size comment goes, and so does the method set after the doc.
		{"```go\ntype Rect struct{ W, H float64 } // size=16 (0x10)\n```\n\n---\n\nRect is an axis-aligned rectangle.\n\n\n" +
			"```go\nfunc (r Rect) Area() float64\n```\n\n---\n\n[`sample.Rect` on pkg.go.dev](https://pkg.go.dev/example.com/sample#Rect)",
			"type Rect struct{ W, H float64 }", "Rect is an axis-aligned rectangle."},
		{"```go\ntype Shape interface { // size=16 (0x10)\n\tArea() float64\n}\n```\n\n---\n\nShape is anything with an area.\n\n\n---\n\n" +
			"[`sample.Shape` on pkg.go.dev](https://pkg.go.dev/example.com/sample#Shape)",
			"type Shape interface {\n\tArea() float64\n}", "Shape is anything with an area."},
		// Size and class on the first line; the author's field comments stay.
		{"```go\ntype Node struct { // size=200 (0xc8), class=208 (0xd0)\n\tID        NodeID\n\tName      string // e.g. \"Build\"\n}\n```\n\n---\n\n" +
			"Node is one symbol in an ego-graph.\n\n\n---\n\n[`graph.Node` on pkg.go.dev](https://pkg.go.dev/x/graph#Node)",
			"type Node struct {\n\tID        NodeID\n\tName      string // e.g. \"Build\"\n}", "Node is one symbol in an ego-graph."},
		// A field: size and offset.
		{"```go\nfield Name string // size=16 (0x10), offset=16 (0x10)\n```\n\n---\n\ne.g. \"Build\"\n\n\n---\n\n" +
			"[`(graph.Node).Name` on pkg.go.dev](https://pkg.go.dev/x/graph#Node.Name)",
			"field Name string", "e.g. \"Build\""},
		// gopls v0.23, verbatim: a field with small values has no hex.
		{"```go\nfield W float64 // size=8, offset=0\n```\n\n---\n\n" +
			"[`(sample.Rect).W` on pkg.go.dev](https://pkg.go.dev/example.com/sample#Rect.W)",
			"field W float64", ""},
		// gopls v0.23, verbatim: a package clause, with and without a doc.
		{"```go\npackage sample\n```\n\n---\n\n - Package path: example.com/sample\n - Module: example.com/sample\n - Language version: go1.26",
			"package sample", ""},
		{"```go\npackage report\n```\n\n---\n\nPackage report describes the sample's shapes.\n\n\n---\n\n" +
			" - Package path: example.com/sample/report\n - Module: example.com/sample\n - Language version: go1.26",
			"package report", "Package report describes the sample's shapes."},
		// A list in the author's doc stays, even one item like gopls's.
		{"```go\nfunc F()\n```\n\nF reads:\n\n - Module: the config\n - Other: more",
			"func F()", "F reads:\n\n- Module: the config\n - Other: more"},
		// A comment that only looks like a size annotation is the author's.
		{"```go\nconst Max = 8 // size=8 bytes\n```", "const Max = 8 // size=8 bytes", ""},
	}
	for _, tc := range tests {
		sig, doc := golang.ParseHover(tc.in)
		if sig != tc.sig || doc != tc.doc {
			t.Errorf("ParseHover(%q) = %q, %q; want %q, %q", tc.in, sig, doc, tc.sig, tc.doc)
		}
	}
}

func TestTypeKeyword(t *testing.T) {
	lines := []string{
		"type Shape interface {",    // top level: widened to "type"
		"\tRect struct{ W, H int }", // in a type ( ... ) group: unchanged
		"func NewRect() Rect {",     // not a type: unchanged
		"type Größe int",            // non-ASCII name, ASCII before it: widened
		"/* é */ type Wide int",     // more than the keyword before the name: unchanged
		"\ttype Local int",          // a local type: widened to "type"
	}
	tests := []struct {
		pos  lsp.Position
		want int
	}{
		{lsp.Position{Line: 0, Character: 5}, 0},
		{lsp.Position{Line: 1, Character: 1}, 1},
		{lsp.Position{Line: 2, Character: 5}, 5},
		{lsp.Position{Line: 3, Character: 5}, 0},
		{lsp.Position{Line: 4, Character: 13}, 13},
		{lsp.Position{Line: 5, Character: 6}, 1},
	}
	for _, tc := range tests {
		got := golang.DeclarationStart(lines[tc.pos.Line], tc.pos)
		if got.Line != tc.pos.Line || got.Character != tc.want {
			t.Errorf("DeclarationStart(%q at %d) = %+v, want character %d", lines[tc.pos.Line], tc.pos.Character, got, tc.want)
		}
	}
}
