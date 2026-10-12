package lspsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/graph"
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
		if s, q := splitName(in); s != want[0] || q != want[1] {
			t.Errorf("splitName(%q) = %q, %q; want %q, %q", in, s, q, want[0], want[1])
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
		// A comment that only looks like a size annotation is the author's.
		{"```go\nconst Max = 8 // size=8 bytes\n```", "const Max = 8 // size=8 bytes", ""},
	}
	for _, tc := range tests {
		sig, doc := parseHover(tc.in)
		if sig != tc.sig || doc != tc.doc {
			t.Errorf("parseHover(%q) = %q, %q; want %q, %q", tc.in, sig, doc, tc.sig, tc.doc)
		}
	}
}

func TestDeclarationLine(t *testing.T) {
	tests := map[string]string{
		"func F() {\n\treturn\n}": "func F()",
		"type T struct{ A int }":  "type T struct{ A int }",
		"class C{\n}":             "class C",
		"":                        "",
	}
	for in, want := range tests {
		if got := declarationLine(in); got != want {
			t.Errorf("declarationLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKindOf(t *testing.T) {
	tests := map[lsp.SymbolKind]graph.SymbolKind{
		lsp.SymbolKindFunction: graph.KindFunction, lsp.SymbolKindConstructor: graph.KindFunction,
		lsp.SymbolKindMethod: graph.KindMethod, lsp.SymbolKindStruct: graph.KindStruct,
		lsp.SymbolKindClass: graph.KindClass, lsp.SymbolKindInterface: graph.KindInterface,
		lsp.SymbolKindEnum: graph.KindType, lsp.SymbolKindTypeParameter: graph.KindType,
		lsp.SymbolKindVariable: graph.KindVariable, lsp.SymbolKindConstant: graph.KindConstant,
		lsp.SymbolKindEnumMember: graph.KindConstant, lsp.SymbolKindField: graph.KindField,
		lsp.SymbolKindProperty: graph.KindField, lsp.SymbolKindModule: graph.KindModule,
		lsp.SymbolKindNamespace: graph.KindModule, lsp.SymbolKindPackage: graph.KindModule,
		lsp.SymbolKindString: graph.KindOther,
	}
	for in, want := range tests {
		if got := kindOf(in); got != want {
			t.Errorf("kindOf(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHardError(t *testing.T) {
	if hardError(nil) != nil {
		t.Error("hardError(nil) != nil")
	}
	if hardError(fmt.Errorf("wrapped: %w", &lsp.RPCError{Code: 1})) != nil {
		t.Error("a server error response should count as no answer")
	}
	broken := errors.New("connection closed")
	if hardError(broken) != broken {
		t.Error("a session failure must be kept")
	}
}

func TestFileText(t *testing.T) {
	f := &file{lines: []string{"a😀bc", "xyz", "end"}}
	// 😀 is two UTF-16 units: character 3 is "b".
	r := lsp.Range{Start: lsp.Position{Line: 0, Character: 3}, End: lsp.Position{Line: 2, Character: 1}}
	if got := f.text(r); got != "bc\nxyz\ne" {
		t.Errorf("text = %q, want %q", got, "bc\nxyz\ne")
	}
	if got := f.toGraph(r); got != (graph.Range{StartLine: 1, StartCol: 3, EndLine: 3, EndCol: 2}) {
		t.Errorf("toGraph = %+v", got)
	}
	if f.line(-1) != "" || f.line(3) != "" {
		t.Error("out-of-range lines should be empty")
	}
	if got := f.text(lsp.Range{Start: lsp.Position{Line: 7}, End: lsp.Position{Line: 7, Character: 4}}); got != "" {
		t.Errorf("text past the end = %q", got)
	}
}

func TestInWorkspace(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, root, uri string
		want            bool
	}{
		{"file in the root", root, lsp.FileURI(filepath.Join(root, "a.go")), true},
		{"file in a subdirectory", root, lsp.FileURI(filepath.Join(root, "sub", "b.go")), true},
		{"sibling sharing the prefix", root, lsp.FileURI(filepath.Join(dir, "root2", "x.go")), false},
		{"parent directory", root, lsp.FileURI(filepath.Join(dir, "x.go")), false},
		{"standard library", root, "file:///usr/local/go/src/fmt/errors.go", false},
		{"not a file URI", root, "untitled:Untitled-1", false},
		{"no root keeps everything", "", "file:///usr/local/go/src/fmt/errors.go", true},
		{"root through a symlink, server reports the real path", link, lsp.FileURI(filepath.Join(real, "a.go")), true},
		{"root through a symlink, path through it", link, lsp.FileURI(filepath.Join(link, "a.go")), true},
	}
	for _, tc := range tests {
		s := New(nil, tc.root)
		if got := s.inWorkspace(tc.uri); got != tc.want {
			t.Errorf("%s: inWorkspace(%q) with root %q = %v, want %v", tc.name, tc.uri, tc.root, got, tc.want)
		}
	}
}

func TestWithKeyword(t *testing.T) {
	f := &file{lines: []string{
		"type Shape interface {",    // top level: widened to "type"
		"\tRect struct{ W, H int }", // in a type ( ... ) group: unchanged
		"func NewRect() Rect {",     // not a type: unchanged
		"type Größe int",            // non-ASCII name, ASCII before it: widened
		"/* é */ type Wide int",     // more than the keyword before the name: unchanged
		"\ttype Local int",          // a local type: widened to "type"
	}}
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
		got := f.withKeyword(tc.pos)
		if got.Line != tc.pos.Line || got.Character != tc.want {
			t.Errorf("withKeyword(%q at %d) = %+v, want character %d", f.lines[tc.pos.Line], tc.pos.Character, got, tc.want)
		}
	}
}
