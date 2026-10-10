package lspsource

import (
	"errors"
	"fmt"
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
