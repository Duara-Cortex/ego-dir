package lspfake_test

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
)

// pythonSession starts the Python scenario, initialises it with the real
// testdata/python root and opens the files named.
func pythonSession(t *testing.T, open ...string) (p *peer, rootURI string) {
	t.Helper()
	root, err := lspfake.PythonRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := lspfake.New()
	lspfake.Python(s)
	p = start(t, s)
	rootURI = (&url.URL{Scheme: "file", Path: filepath.ToSlash(root)}).String()
	if m := p.call(0, "initialize", map[string]any{"rootUri": rootURI}); m.Error != nil {
		t.Fatalf("initialize: %v", m.Error)
	}
	for _, rel := range open {
		text, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		p.send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
			"textDocument": map[string]any{"uri": rootURI + "/" + rel, "languageId": "python", "version": 1, "text": string(text)}}})
	}
	return p, rootURI
}

// sameJSON reports whether two JSON texts hold the same value.
func sameJSON(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("result %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want %s: %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}

// pyright answers, as pyright 1.1.414 gave them for testdata/python with
// every file open. ROOT is the workspace root and TYPESHED pyright's
// bundled stubs; the JSON is compared by value, not by key order.
func TestPythonMatchesPyright(t *testing.T) {
	p, rootURI := pythonSession(t, lspfake.PythonFile, lspfake.PythonReportFile, lspfake.PythonPackageFile)
	S, R := rootURI+"/"+lspfake.PythonFile, rootURI+"/"+lspfake.PythonReportFile
	rng := func(l, a, b int) string { return rangeJSON(l, a, b) }
	loc := func(u string, l, a, b int) string { return `{"uri":"` + u + `","range":` + rng(l, a, b) + `}` }
	item := func(name string, kind int, detail, u string, l, a, b int) string {
		return `{"name":"` + name + `","kind":` + itoa(kind) + `,"detail":"` + detail + `","uri":"` + u + `","range":` + rng(l, a, b) + `,"selectionRange":` + rng(l, a, b) + `}`
	}
	list := func(xs ...string) string { return "[" + strings.Join(xs, ",") + "]" }
	hover := func(value string, l, a, b int) string {
		v, _ := json.Marshal(value)
		return `{"contents":{"kind":"markdown","value":` + string(v) + `},"range":` + rng(l, a, b) + `}`
	}
	typeshed := lspfake.PythonTypeshedURI
	shape := item("Shape", 5, "(shapes.py)", S, 5, 6, 11)
	shapeArea := item("area", 6, "class Shape (shapes.py)", S, 9, 8, 12)
	rect := item("Rect", 5, "(shapes.py)", S, 13, 6, 10)
	newRect := item("new_rect", 12, "(shapes.py)", S, 25, 4, 12)
	totalArea := item("total_area", 12, "(shapes.py)", S, 30, 4, 14)
	report := item("report", 12, "(shapes.py)", S, 38, 4, 10)
	workspace := func(it string) string { return strings.Replace(it, `)","uri"`, `) · Workspace","uri"`, 1) }

	tests := []struct {
		name, method, uri string
		line, char        int
		want              string // "null" for null
	}{
		{"definition Shape", "textDocument/definition", S, 5, 6, list(loc(S, 5, 6, 11))},
		{"typeDefinition Shape", "textDocument/typeDefinition", S, 5, 6, list(loc(S, 5, 6, 11))},
		{"references Shape", "textDocument/references", S, 5, 6, list(loc(S, 13, 11, 16), loc(S, 30, 28, 33))},
		{"hover Shape", "textDocument/hover", S, 5, 6, hover("```python\nclass Shape()\n```\n---\nShape is anything with an area.", 5, 6, 11)},
		{"prepare Shape", "textDocument/prepareCallHierarchy", S, 5, 6, list(shape)},

		{"definition Shape.area", "textDocument/definition", S, 9, 8, list(loc(S, 9, 8, 12))},
		{"typeDefinition Shape.area", "textDocument/typeDefinition", S, 9, 8, list(loc(S, 9, 8, 12))},
		{"references Shape.area", "textDocument/references", S, 9, 8, list(loc(S, 34, 19, 23))},
		{"hover Shape.area", "textDocument/hover", S, 9, 8,
			hover("```python\n(method) def area(self: Self@Shape) -> float\n```\n---\nReturn the shape's area.", 9, 8, 12)},
		{"prepare Shape.area", "textDocument/prepareCallHierarchy", S, 9, 8, list(shapeArea)},

		{"definition Rect", "textDocument/definition", S, 13, 6, list(loc(S, 13, 6, 10))},
		{"references Rect", "textDocument/references", S, 13, 6,
			list(loc(S, 25, 36, 40), loc(S, 27, 11, 15), loc(R, 4, 19, 23), loc(R, 7, 16, 20))},
		{"hover Rect", "textDocument/hover", S, 13, 6,
			hover("```python\nclass Rect(\n    w: float,\n    h: float\n)\n```\n---\nRect is an axis-aligned rectangle.", 13, 6, 10)},
		{"prepare Rect", "textDocument/prepareCallHierarchy", S, 13, 6, list(rect)},

		{"references Rect.__init__", "textDocument/references", S, 16, 8, "[]"},
		{"hover Rect.__init__", "textDocument/hover", S, 16, 8,
			hover("```python\n(method) def __init__(\n    self: Self@Rect,\n    w: float,\n    h: float\n) -> None\n```", 16, 8, 16)},
		{"prepare Rect.__init__", "textDocument/prepareCallHierarchy", S, 16, 8, list(item("__init__", 6, "class Rect (shapes.py)", S, 16, 8, 16))},

		{"definition Rect.w", "textDocument/definition", S, 17, 13, list(loc(S, 17, 13, 14))},
		{"typeDefinition Rect.w", "textDocument/typeDefinition", S, 17, 13, list(loc(typeshed+"/builtins.pyi", 365, 6, 11))},
		{"references Rect.w", "textDocument/references", S, 17, 13, list(loc(S, 22, 20, 21), loc(R, 9, 24, 25))},
		{"hover Rect.w", "textDocument/hover", S, 17, 13, hover("```python\n(variable) w: float\n```", 17, 13, 14)},
		{"prepare Rect.w", "textDocument/prepareCallHierarchy", S, 17, 13, "null"},
		{"references Rect.h", "textDocument/references", S, 18, 13, list(loc(S, 22, 29, 30), loc(R, 9, 29, 30))},
		{"hover Rect.h", "textDocument/hover", S, 18, 13, hover("```python\n(variable) h: float\n```", 18, 13, 14)},

		{"references Rect.area", "textDocument/references", S, 20, 8, "[]"},
		{"hover Rect.area", "textDocument/hover", S, 20, 8,
			hover("```python\n(method) def area(self: Self@Rect) -> float\n```\n---\nReturn the rectangle's area.", 20, 8, 12)},
		{"prepare Rect.area", "textDocument/prepareCallHierarchy", S, 20, 8, list(item("area", 6, "class Rect (shapes.py)", S, 20, 8, 12))},

		{"typeDefinition new_rect", "textDocument/typeDefinition", S, 25, 4, list(loc(S, 25, 4, 12))},
		{"references new_rect", "textDocument/references", S, 25, 4, list(loc(S, 40, 23, 31), loc(S, 40, 39, 47))},
		{"hover new_rect", "textDocument/hover", S, 25, 4,
			hover("```python\n(function) def new_rect(\n    w: float,\n    h: float\n) -> Rect\n```\n---\nBuild a Rect, like new\\_rect(1, 2).", 25, 4, 12)},
		{"references total_area", "textDocument/references", S, 30, 4, list(loc(S, 40, 11, 21))},
		{"hover total_area", "textDocument/hover", S, 30, 4,
			hover("```python\n(function) def total_area(shapes: list[Shape]) -> float\n```\n---\nSum the area of every shape.", 30, 4, 14)},
		{"references report", "textDocument/references", S, 38, 4, "[]"},
		{"hover report", "textDocument/hover", S, 38, 4,
			hover("```python\n(function) def report() -> float\n```\n---\nReturn the total area of two rectangles.", 38, 4, 10)},

		{"definition describe", "textDocument/definition", R, 7, 4, list(loc(R, 7, 4, 12))},
		{"references describe", "textDocument/references", R, 7, 4, "[]"},
		{"hover describe", "textDocument/hover", R, 7, 4,
			hover("```python\n(function) def describe(r: Rect) -> str\n```\n---\nFormat a rectangle's size.", 7, 4, 12)},
		{"prepare describe", "textDocument/prepareCallHierarchy", R, 7, 4, list(item("describe", 12, "(report.py)", R, 7, 4, 12))},
		{"hover label", "textDocument/hover", R, 12, 4,
			hover("```python\n(function) def label(s: str) -> str\n```\n---\nCapitalise each word of a label.", 12, 4, 9)},
		{"prepare label", "textDocument/prepareCallHierarchy", R, 12, 4, list(item("label", 12, "(report.py)", R, 12, 4, 9))},

		// At uses, the declaration is in the answer and the use asked about is not.
		{"definition Rect() call", "textDocument/definition", S, 27, 11, list(loc(S, 13, 6, 10))},
		{"references Rect() call", "textDocument/references", S, 27, 11,
			list(loc(S, 13, 6, 10), loc(S, 25, 36, 40), loc(R, 4, 19, 23), loc(R, 7, 16, 20))},
		{"prepare Rect() call", "textDocument/prepareCallHierarchy", S, 27, 11, list(rect)},
		{"definition s.area call", "textDocument/definition", S, 34, 19, list(loc(S, 9, 8, 12))},
		{"references s.area call", "textDocument/references", S, 34, 19, list(loc(S, 9, 8, 12))},
		{"prepare s.area call", "textDocument/prepareCallHierarchy", S, 34, 19, list(shapeArea)},
		{"references total_area call site", "textDocument/references", S, 40, 11, list(loc(S, 30, 4, 14))},
		{"references new_rect call site", "textDocument/references", S, 40, 23, list(loc(S, 25, 4, 12), loc(S, 40, 39, 47))},
		{"references self.w use", "textDocument/references", S, 22, 20, list(loc(S, 17, 13, 14), loc(R, 9, 24, 25))},
		{"definition Rect import", "textDocument/definition", R, 4, 19, list(loc(S, 13, 6, 10))},
		{"references Rect import", "textDocument/references", R, 4, 19,
			list(loc(S, 13, 6, 10), loc(S, 25, 36, 40), loc(S, 27, 11, 15), loc(R, 7, 16, 20))},
		{"prepare Rect import", "textDocument/prepareCallHierarchy", R, 4, 19, list(rect)},
		{"references Rect annotation", "textDocument/references", R, 7, 16,
			list(loc(S, 13, 6, 10), loc(S, 25, 36, 40), loc(S, 27, 11, 15), loc(R, 4, 19, 23))},
		{"definition r.w use", "textDocument/definition", R, 9, 24, list(loc(S, 17, 13, 14))},
		{"references r.w use", "textDocument/references", R, 9, 24, list(loc(S, 17, 13, 14), loc(S, 22, 20, 21))},

		// A docstring is nothing pyright answers about.
		{"definition module docstring", "textDocument/definition", S, 0, 0, "null"},
		{"hover module docstring", "textDocument/hover", S, 0, 0, "null"},
		{"prepare module docstring", "textDocument/prepareCallHierarchy", S, 0, 0, "null"},
		{"hover package docstring", "textDocument/hover", rootURI + "/" + lspfake.PythonPackageFile, 0, 0, "null"},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := at(tc.uri, tc.line, tc.char)
			if tc.method == "textDocument/references" {
				params = withContext(params, false)
			}
			m := p.call(10+i, tc.method, params)
			if m.Error != nil || !sameJSON(t, m.Result, tc.want) {
				t.Errorf("%s = %s (err %v)\nwant %s", tc.method, m.Result, m.Error, tc.want)
			}
		})
	}

	calls := []struct {
		name, method, item, want string
	}{
		{"incoming Shape", "callHierarchy/incomingCalls", shape, "null"},
		{"outgoing Shape", "callHierarchy/outgoingCalls", shape, "null"},
		// A call through an attribute is listed twice.
		{"incoming Shape.area", "callHierarchy/incomingCalls", shapeArea,
			`[{"from":` + totalArea + `,"fromRanges":[` + rng(34, 19, 23) + `,` + rng(34, 19, 23) + `]}]`},
		{"outgoing Shape.area", "callHierarchy/outgoingCalls", shapeArea, "null"},
		{"incoming Rect", "callHierarchy/incomingCalls", rect, `[{"from":` + newRect + `,"fromRanges":[` + rng(27, 11, 15) + `]}]`},
		{"outgoing Rect", "callHierarchy/outgoingCalls", rect, "null"},
		{"incoming Rect.area", "callHierarchy/incomingCalls", item("area", 6, "class Rect (shapes.py)", S, 20, 8, 12), "null"},
		{"incoming new_rect", "callHierarchy/incomingCalls", newRect,
			`[{"from":` + report + `,"fromRanges":[` + rng(40, 23, 31) + `,` + rng(40, 39, 47) + `]}]`},
		{"outgoing new_rect", "callHierarchy/outgoingCalls", newRect, `[{"to":` + workspace(rect) + `,"fromRanges":[` + rng(27, 11, 15) + `]}]`},
		{"incoming total_area", "callHierarchy/incomingCalls", totalArea, `[{"from":` + report + `,"fromRanges":[` + rng(40, 11, 21) + `]}]`},
		{"outgoing total_area", "callHierarchy/outgoingCalls", totalArea, `[{"to":` + workspace(shapeArea) + `,"fromRanges":[` + rng(34, 19, 23) + `]}]`},
		{"incoming report", "callHierarchy/incomingCalls", report, "null"},
		{"outgoing report", "callHierarchy/outgoingCalls", report,
			`[{"to":` + workspace(totalArea) + `,"fromRanges":[` + rng(40, 11, 21) + `]},{"to":` + workspace(newRect) + `,"fromRanges":[` + rng(40, 23, 31) + `,` + rng(40, 39, 47) + `]}]`},
		{"outgoing describe", "callHierarchy/outgoingCalls", item("describe", 12, "(report.py)", R, 7, 4, 12), "null"},
		{"outgoing label", "callHierarchy/outgoingCalls", item("label", 12, "(report.py)", R, 12, 4, 9),
			`[{"to":` + item("capwords", 12, "(__init__.pyi) · Standard library", typeshed+"/string/__init__.pyi", 32, 4, 12) + `,"fromRanges":[` + rng(14, 18, 26) + `]}]`},
	}
	for i, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			m := p.call(200+i, tc.method, map[string]any{"item": json.RawMessage(tc.item)})
			if m.Error != nil || !sameJSON(t, m.Result, tc.want) {
				t.Errorf("%s = %s (err %v)\nwant %s", tc.method, m.Result, m.Error, tc.want)
			}
		})
	}

	ds := func(name string, kind int, sl, sc, el, ec int, sel string, children ...string) string {
		r := `{"start":{"line":` + itoa(sl) + `,"character":` + itoa(sc) + `},"end":{"line":` + itoa(el) + `,"character":` + itoa(ec) + `}}`
		return `{"name":"` + name + `","kind":` + itoa(kind) + `,"range":` + r + `,"selectionRange":` + sel + `,"children":[` + strings.Join(children, ",") + `]}`
	}
	same := func(name string, kind, l, a, b int) string { return ds(name, kind, l, a, l, b, rng(l, a, b)) }
	outlines := map[string]string{
		S: list(
			ds("Shape", 5, 5, 0, 10, 38, rng(5, 6, 11), ds("area", 6, 8, 4, 10, 38, rng(9, 8, 12))),
			ds("Rect", 5, 13, 0, 22, 30, rng(13, 6, 10),
				ds("__init__", 6, 16, 4, 18, 18, rng(16, 8, 16), same("w", 13, 16, 23, 31), same("h", 13, 16, 33, 41)),
				ds("area", 6, 20, 4, 22, 30, rng(20, 8, 12)),
				same("w", 13, 17, 13, 14), same("h", 13, 18, 13, 14)),
			ds("new_rect", 12, 25, 0, 27, 21, rng(25, 4, 12), same("w", 13, 25, 13, 21), same("h", 13, 25, 23, 31)),
			ds("total_area", 12, 30, 0, 35, 16, rng(30, 4, 14), same("shapes", 13, 30, 15, 34), same("total", 13, 32, 4, 9), same("s", 13, 33, 8, 9)),
			ds("report", 12, 38, 0, 40, 55, rng(38, 4, 10))),
		R: list(
			ds("describe", 12, 7, 0, 9, 31, rng(7, 4, 12), same("r", 13, 7, 13, 20)),
			ds("label", 12, 12, 0, 14, 29, rng(12, 4, 9), same("s", 13, 12, 10, 16))),
		rootURI + "/" + lspfake.PythonPackageFile: "[]",
	}
	i := 0
	for uri, want := range outlines {
		m := p.call(300+i, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": uri}})
		if m.Error != nil || !sameJSON(t, m.Result, want) {
			t.Errorf("documentSymbol(%s) = %s (err %v)\nwant %s", uri, m.Result, m.Error, want)
		}
		i++
	}
}

func TestPythonRefusesWhatPyrightRefuses(t *testing.T) {
	root, err := lspfake.PythonRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := lspfake.New()
	lspfake.Python(s)
	p := start(t, s)
	rootURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(root)}).String()
	if m := p.call(1, "initialise", map[string]any{"rootUri": rootURI}); m.Error == nil || m.Error.Code != lspfake.CodeMethodNotFound ||
		m.Error.Message != "Unhandled method initialise" {
		t.Errorf("initialise = %+v, want pyright's -32601", m)
	}
	m := p.call(2, "initialize", map[string]any{"rootUri": rootURI})
	if m.Error != nil || strings.Contains(string(m.Result), "implementationProvider") || !strings.Contains(string(m.Result), `"callHierarchyProvider":true`) {
		t.Errorf("initialize = %s (err %v), want call hierarchy and no implementation", m.Result, m.Error)
	}
	uri := rootURI + "/" + lspfake.PythonFile
	p.send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": "python", "version": 1, "text": ""}}})
	m = p.call(3, "textDocument/implementation", at(uri, 5, 6))
	if m.Error == nil || m.Error.Code != lspfake.CodeMethodNotFound || m.Error.Message != "Unhandled method textDocument/implementation" {
		t.Errorf("implementation = %+v, want pyright's -32601", m)
	}
}

// TestPythonAnswersFromOpenFilesOnly is what pyright does before it has
// analysed the workspace: with only shapes.py open, it does not see the
// uses in report/report.py, and says nothing about a file that is closed.
func TestPythonAnswersFromOpenFilesOnly(t *testing.T) {
	p, rootURI := pythonSession(t, lspfake.PythonFile)
	S, R := rootURI+"/"+lspfake.PythonFile, rootURI+"/"+lspfake.PythonReportFile
	loc := func(u string, l, a, b int) string { return `{"uri":"` + u + `","range":` + rangeJSON(l, a, b) + `}` }
	for i, tc := range []struct {
		name, method, uri string
		line, char        int
		want              string
	}{
		{"references Rect", "textDocument/references", S, 13, 6, "[" + loc(S, 25, 36, 40) + "," + loc(S, 27, 11, 15) + "]"},
		{"references Rect.w", "textDocument/references", S, 17, 13, "[" + loc(S, 22, 20, 21) + "]"},
		{"definition describe", "textDocument/definition", R, 7, 4, "null"},
		{"hover describe", "textDocument/hover", R, 7, 4, "null"},
		{"prepare label", "textDocument/prepareCallHierarchy", R, 12, 4, "null"},
	} {
		params := at(tc.uri, tc.line, tc.char)
		if tc.method == "textDocument/references" {
			params = withContext(params, false)
		}
		if m := p.call(10+i, tc.method, params); m.Error != nil || !sameJSON(t, m.Result, tc.want) {
			t.Errorf("%s: %s = %s (err %v)\nwant %s", tc.name, tc.method, m.Result, m.Error, tc.want)
		}
	}

	// Opening report.py brings its uses in; closing it takes them away.
	text, err := os.ReadFile(filepath.Join(strings.TrimPrefix(rootURI, "file://"), filepath.FromSlash(lspfake.PythonReportFile)))
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"uri": R, "languageId": "python", "version": 1, "text": string(text)}
	p.send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{"textDocument": doc}})
	if m := p.call(20, "textDocument/references", withContext(at(S, 17, 13), false)); !sameJSON(t, m.Result, "["+loc(S, 22, 20, 21)+","+loc(R, 9, 24, 25)+"]") {
		t.Errorf("references Rect.w with report.py open = %s", m.Result)
	}
	p.send(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didClose", "params": map[string]any{"textDocument": map[string]any{"uri": R}}})
	if m := p.call(21, "textDocument/references", withContext(at(S, 17, 13), false)); !sameJSON(t, m.Result, "["+loc(S, 22, 20, 21)+"]") {
		t.Errorf("references Rect.w after closing report.py = %s", m.Result)
	}
	// Callers, too, come only from open files: shapes.py is open, so these are here.
	item := `{"name":"new_rect","kind":12,"detail":"(shapes.py)","uri":"` + S + `","range":` + rangeJSON(25, 4, 12) + `,"selectionRange":` + rangeJSON(25, 4, 12) + `}`
	if m := p.call(22, "callHierarchy/incomingCalls", map[string]any{"item": json.RawMessage(item)}); m.Error != nil || !strings.Contains(string(m.Result), `"name":"report"`) {
		t.Errorf("incomingCalls of new_rect = %s (err %v)", m.Result, m.Error)
	}
}
