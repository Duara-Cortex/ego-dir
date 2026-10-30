package lspsource

import (
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// The hover strings are pyright 1.1.414's answers for testdata/python,
// verbatim.
func TestPyrightHover(t *testing.T) {
	tests := []struct{ name, in, sig, doc string }{
		{"function", "```python\n(function) def total_area(shapes: list[Shape]) -> float\n```\n---\nSum the area of every shape.",
			"def total_area(shapes: list[Shape]) -> float", "Sum the area of every shape."},
		{"function over several lines, escaped doc",
			"```python\n(function) def new_rect(\n    w: float,\n    h: float\n) -> Rect\n```\n---\nBuild a Rect, like new\\_rect(1, 2).",
			"def new_rect(w: float, h: float) -> Rect", "Build a Rect, like new_rect(1, 2)."},
		{"function without parameters", "```python\n(function) def report() -> float\n```\n---\nReturn the total area of two rectangles.",
			"def report() -> float", "Return the total area of two rectangles."},
		{"class with a constructor", "```python\nclass Rect(\n    w: float,\n    h: float\n)\n```\n---\nRect is an axis-aligned rectangle.",
			"class Rect(w: float, h: float)", "Rect is an axis-aligned rectangle."},
		{"class without one", "```python\nclass Shape()\n```\n---\nShape is anything with an area.",
			"class Shape()", "Shape is anything with an area."},
		{"method", "```python\n(method) def area(self: Self@Shape) -> float\n```\n---\nReturn the shape's area.",
			"def area(self: Self@Shape) -> float", "Return the shape's area."},
		{"method without a docstring",
			"```python\n(method) def __init__(\n    self: Self@Rect,\n    w: float,\n    h: float\n) -> None\n```",
			"def __init__(self: Self@Rect, w: float, h: float) -> None", ""},
		{"attribute", "```python\n(variable) w: float\n```", "w: float", ""},
		{"module", "```python\n(module) shapes\n```\n---\nShapes and their areas.", "shapes", "Shapes and their areas."},
		{"standard library, escaped",
			"```python\n(function) def capwords(\n    s: StrOrLiteralStr@capwords,\n    sep: StrOrLiteralStr@capwords | None = None\n) -> StrOrLiteralStr@capwords\n```\n---\n" +
				"capwords(s \\[,sep\\]) -&gt; string\n\nSplit the argument into words using split, capitalize each\nword using capitalize, and join the capitalized words using\n" +
				"join.  If the optional second argument sep is absent or None,\nruns of whitespace characters are replaced by a single space\n" +
				"and leading and trailing whitespace are removed, otherwise\nsep is used to split and join the words.",
			"def capwords(s: StrOrLiteralStr@capwords, sep: StrOrLiteralStr@capwords | None = None) -> StrOrLiteralStr@capwords",
			"capwords(s [,sep]) -> string\n\nSplit the argument into words using split, capitalize each\nword using capitalize, and join the capitalized words using\n" +
				"join.  If the optional second argument sep is absent or None,\nruns of whitespace characters are replaced by a single space\n" +
				"and leading and trailing whitespace are removed, otherwise\nsep is used to split and join the words."},
		{"indentation as entities, breaks as trailing spaces",
			"```python\n(function) def abstractmethod(funcobj: _FuncT@abstractmethod) -> _FuncT@abstractmethod\n```\n---\n" +
				"Usage:\n\n&nbsp;&nbsp;&nbsp;&nbsp;class C(metaclass=ABCMeta):  \n&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;@abstractmethod\n" +
				"def my\\_abstract\\_method(self, arg1, arg2, argN):  \n&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;...",
			"def abstractmethod(funcobj: _FuncT@abstractmethod) -> _FuncT@abstractmethod",
			"Usage:\n\n    class C(metaclass=ABCMeta):\n        @abstractmethod\ndef my_abstract_method(self, arg1, arg2, argN):\n            ..."},
		{"no hover", "", "", ""},
		{"prose only", "Just text.", "", "Just text."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if sig, doc := python.ParseHover(tc.in); sig != tc.sig || doc != tc.doc {
				t.Errorf("ParseHover(%q)\n= %q, %q\nwant %q, %q", tc.in, sig, doc, tc.sig, tc.doc)
			}
		})
	}
}

func TestPythonUsesGenericNamesAndDeclarations(t *testing.T) {
	if s, q := python.SplitName("area"); s != "area" || q != "" {
		t.Errorf("SplitName = %q, %q", s, q)
	}
	if o := python.Owner("area"); o != "" {
		t.Errorf("Owner = %q, want none: methods are nested in the outline", o)
	}
	// pyright starts a decorated method at its decorator, a class at "class".
	start := lsp.Position{Line: 8, Character: 4}
	if got := python.DeclarationStart("    @abstractmethod", start); got != start {
		t.Errorf("DeclarationStart = %+v, want the server's %+v", got, start)
	}
}

func TestPyOutline(t *testing.T) {
	sym := func(name string, kind lsp.SymbolKind) lsp.DocumentSymbol {
		return lsp.DocumentSymbol{Name: name, Kind: kind, Range: lspRange(1, 0, 5), SelectionRange: lspRange(1, 0, 1)}
	}
	class, function, method := sym("Rect", lsp.SymbolKindClass), sym("new_rect", lsp.SymbolKindFunction), sym("area", lsp.SymbolKindMethod)
	tests := []struct {
		name     string
		sym      lsp.DocumentSymbol
		parent   *lsp.DocumentSymbol
		keep     bool
		wantKind lsp.SymbolKind
	}{
		{"module-level variable", sym("PI", lsp.SymbolKindVariable), nil, true, lsp.SymbolKindVariable},
		{"top-level class", class, nil, true, lsp.SymbolKindClass},
		{"method of a class", method, &class, true, lsp.SymbolKindMethod},
		{"attribute of a class", sym("w", lsp.SymbolKindVariable), &class, true, lsp.SymbolKindField},
		{"parameter of a function", sym("w", lsp.SymbolKindVariable), &function, false, 0},
		{"local of a method", sym("total", lsp.SymbolKindVariable), &method, false, 0},
		{"nested function", sym("helper", lsp.SymbolKindFunction), &function, true, lsp.SymbolKindFunction},
	}
	for _, tc := range tests {
		got, keep := python.OutlineEntry(tc.sym, tc.parent)
		if keep != tc.keep || keep && got.Kind != tc.wantKind {
			t.Errorf("%s: OutlineEntry = kind %d, keep %v; want kind %d, keep %v", tc.name, got.Kind, keep, tc.wantKind, tc.keep)
		}
		if keep && (got.Name != tc.sym.Name || got.Range != tc.sym.Range || got.SelectionRange != tc.sym.SelectionRange) {
			t.Errorf("%s: OutlineEntry changed more than the kind: %+v", tc.name, got)
		}
	}
	// Go and the generic parts keep the outline as it is.
	for _, lang := range []Language{golang, generic} {
		if got, keep := lang.OutlineEntry(sym("w", lsp.SymbolKindVariable), &function); !keep || got.Kind != lsp.SymbolKindVariable {
			t.Errorf("%T: OutlineEntry = %+v, %v; want it unchanged", lang, got, keep)
		}
	}
}

func TestOneLine(t *testing.T) {
	tests := map[string]string{
		"def f() -> int": "def f() -> int",
		"def f(\n    a: int,\n    b: int\n) -> int": "def f(a: int, b: int) -> int",
		"class C(\n    w: float\n)":                 "class C(w: float)",
	}
	for in, want := range tests {
		if got := oneLine(in); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
}
