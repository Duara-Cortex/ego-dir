package lspsource

import (
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

func TestLanguageOf(t *testing.T) {
	tests := map[string]Language{
		"file:///w/shapes.go":    golang,
		"file:///w/SHAPES.GO":    golang,
		"file:///w/app.py":       generic,
		"file:///w/README":       generic,
		"untitled:Untitled-1.go": generic,
	}
	for uri, want := range tests {
		if got := languageOf(uri); got != want {
			t.Errorf("languageOf(%q) = %T, want %T", uri, got, want)
		}
	}
}

func TestGenericNames(t *testing.T) {
	// Without a language of its own, an outline name is taken as it is.
	if s, q := generic.SplitName("(Rect).Area"); s != "(Rect).Area" || q != "" {
		t.Errorf("SplitName = %q, %q", s, q)
	}
	if o := generic.Owner("(Rect).Area"); o != "" {
		t.Errorf("Owner = %q, want none", o)
	}
}

func TestGenericDeclarationStart(t *testing.T) {
	start := lsp.Position{Line: 3, Character: 5}
	if got := generic.DeclarationStart("type Shape interface {", start); got != start {
		t.Errorf("DeclarationStart = %+v, want the server's %+v", got, start)
	}
}

func TestGenericHover(t *testing.T) {
	tests := []struct{ in, sig, doc string }{
		{"```python\ndef f() -> int\n```\n\n---\n\nf does it.\n\nMore.", "def f() -> int", "f does it.\n\nMore."},
		// No server-specific cleaning: links and later code stay.
		{"```ts\nfunction f(): void\n```\n\n[docs](https://example.com)", "function f(): void", "[docs](https://example.com)"},
		{"plain text only", "", "plain text only"},
		{"```go\nunterminated", "", "```go\nunterminated"},
		{"", "", ""},
	}
	for _, tc := range tests {
		if sig, doc := generic.ParseHover(tc.in); sig != tc.sig || doc != tc.doc {
			t.Errorf("ParseHover(%q) = %q, %q; want %q, %q", tc.in, sig, doc, tc.sig, tc.doc)
		}
	}
}

func TestGenericHasNoModules(t *testing.T) {
	if _, ok, err := generic.ModuleOf("/w/app.py"); ok || err != nil {
		t.Errorf("ModuleOf = %v, %v; want no module", ok, err)
	}
	if imports, err := generic.Imports(Module{Path: "app"}); imports != nil || err != nil {
		t.Errorf("Imports = %v, %v; want none", imports, err)
	}
}
