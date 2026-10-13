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
