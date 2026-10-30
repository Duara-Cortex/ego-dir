package lspfake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// PythonFile is the main file of the Python workspace (testdata/python),
// module shapes.
const PythonFile = "shapes.py"

// PythonReportFile is module report.report, which imports shapes and the
// standard library's string. The path is relative to the root, with
// slashes.
const PythonReportFile = "report/report.py"

// PythonPackageFile is the report package, a docstring and nothing else.
const PythonPackageFile = "report/__init__.py"

// pythonFiles are the files of the Python workspace, in the order pyright
// lists answers from them.
var pythonFiles = []string{PythonFile, PythonReportFile, PythonPackageFile}

// PythonTypeshedURI is where the scenario says pyright keeps its bundled
// standard library stubs. It is outside the workspace and does not exist.
const PythonTypeshedURI = "file:///lspfake-pyright/typeshed-fallback/stdlib"

// PythonRoot returns the absolute path of testdata/python, walking up from
// the current directory.
func PythonRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, "testdata", "python")
		if _, err := os.Stat(filepath.Join(p, PythonFile)); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("lspfake: testdata/python not found")
		}
		dir = parent
	}
}

// Python scripts s to answer like pyright 1.1.414 for the Python
// workspace, computing every position from the files on disk. Like
// pyright, it:
//   - refuses initialise and textDocument/implementation as unhandled
//     methods (-32601), and does not offer implementation;
//   - answers only about files that are open: a request on a closed file
//     gets null, and references and callers come only from open files, as
//     pyright gives them before it has analysed the rest of the workspace;
//   - nests methods, then attributes (self.w), in their class in the
//     outline, and parameters and local variables in their function; starts
//     a declaration at its keyword or decorator, and ends it at its last
//     line;
//   - gives call hierarchy items the name as their range and no data, and
//     answers prepareCallHierarchy on a class too;
//   - lists a call through an attribute (s.area()) twice in fromRanges of
//     incoming calls;
//   - resolves s.area() to Shape.area only: nothing calls Rect.area;
//   - answers typeDefinition of a function or class with itself, and of
//     an attribute with float in its bundled stubs;
//   - leaves out the occurrence asked about from references, rather than
//     the declaration;
//   - answers an empty list or null, never an error, when there is nothing;
//   - hovers with the kind in parentheses, a rule straight after the code
//     block and the docstring escaped for Markdown.
//
// Hover at a call site gives the declaration's signature (pyright shows a
// method there without self), and parameters and local variables have no
// hover: ego-dir asks for neither.
func Python(s *Server) {
	sc := &pyScenario{}
	unhandled := func(method string) Handler {
		return func(context.Context, json.RawMessage) (any, error) {
			return nil, &Error{Code: CodeMethodNotFound, Message: "Unhandled method " + method}
		}
	}
	s.Handle("initialise", unhandled("initialise"))
	s.Handle("initialize", func(_ context.Context, p json.RawMessage) (any, error) {
		if err := sc.load(p); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
		}
		progress := map[string]any{"workDoneProgress": true}
		return map[string]any{"capabilities": map[string]any{
			"textDocumentSync": 2, "definitionProvider": progress, "declarationProvider": progress,
			"typeDefinitionProvider": progress, "referencesProvider": progress, "documentSymbolProvider": progress,
			"workspaceSymbolProvider": progress, "hoverProvider": progress, "callHierarchyProvider": true,
			"workspace": map[string]any{"workspaceFolders": map[string]any{"supported": true, "changeNotifications": true}},
		}}, nil
	})
	s.Handle("initialized", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	s.Handle("textDocument/implementation", unhandled("textDocument/implementation"))
	s.Handle("textDocument/didOpen", sc.opening(true))
	s.Handle("textDocument/didClose", sc.opening(false))
	s.Handle("textDocument/didChange", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	s.Handle("textDocument/definition", sc.positional(func(sym *pySym, _ position, _ json.RawMessage) (any, error) {
		return []location{sym.location(sym.sel)}, nil
	}))
	s.Handle("textDocument/typeDefinition", sc.positional(func(sym *pySym, _ position, _ json.RawMessage) (any, error) {
		switch t := sc.syms[sym.typeOf]; {
		case sym.typeOf == "float":
			return []location{{URI: PythonTypeshedURI + "/builtins.pyi", Range: span{position{365, 6}, position{365, 11}}}}, nil
		case t != nil:
			return []location{t.location(t.sel)}, nil
		}
		return []location{sym.location(sym.sel)}, nil
	}))
	s.Handle("textDocument/references", sc.positional(func(sym *pySym, pos position, p json.RawMessage) (any, error) {
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Context struct {
				IncludeDeclaration bool `json:"includeDeclaration"`
			} `json:"context"`
		}
		_ = json.Unmarshal(p, &params)
		out := []location{}
		for _, loc := range append([]location{sym.location(sym.sel)}, sc.uses(sym)...) {
			queried := loc.URI == params.TextDocument.URI && loc.Range.contains(pos)
			if (params.Context.IncludeDeclaration || !queried) && sc.open[loc.URI] {
				out = append(out, loc)
			}
		}
		return out, nil
	}))
	s.Handle("textDocument/hover", sc.positional(func(sym *pySym, _ position, _ json.RawMessage) (any, error) {
		if sym.signature == "" {
			return nil, nil
		}
		value := "```python\n" + sym.signature + "\n```"
		if sym.doc != "" {
			value += "\n---\n" + pyMarkdownEscaper.Replace(sym.doc)
		}
		return map[string]any{"contents": map[string]any{"kind": "markdown", "value": value}, "range": sym.sel}, nil
	}))
	s.Handle("textDocument/prepareCallHierarchy", sc.positional(func(sym *pySym, _ position, _ json.RawMessage) (any, error) {
		if !sym.callable() {
			return nil, nil
		}
		return []pyItem{sym.item("")}, nil
	}))
	s.Handle("callHierarchy/incomingCalls", sc.hierarchy(func(sym *pySym) any {
		var out []map[string]any
		for _, c := range sc.calls {
			if c.to != sym.key || !sc.open[sc.syms[c.from].file.uri] {
				continue
			}
			sites := c.sites
			if c.viaAttribute {
				sites = nil
				for _, site := range c.sites {
					sites = append(sites, site, site)
				}
			}
			out = append(out, map[string]any{"from": sc.syms[c.from].item(""), "fromRanges": sites})
		}
		if out == nil {
			return nil
		}
		return out
	}))
	s.Handle("callHierarchy/outgoingCalls", sc.hierarchy(func(sym *pySym) any {
		var out []map[string]any
		for _, c := range sc.calls {
			if c.from != sym.key {
				continue
			}
			var to pyItem
			if ext, ok := pythonExternals[c.to]; ok {
				to = pyItem{Name: ext.name, Kind: KindFunction, Detail: ext.detail, URI: ext.uri, Range: ext.sel, SelectionRange: ext.sel}
			} else {
				to = sc.syms[c.to].item(" · Workspace")
			}
			out = append(out, map[string]any{"to": to, "fromRanges": c.sites})
		}
		if out == nil {
			return nil
		}
		return out
	}))
	s.Handle("textDocument/documentSymbol", func(_ context.Context, p json.RawMessage) (any, error) {
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(p, &params)
		sc.mu.Lock()
		defer sc.mu.Unlock()
		if sc.syms == nil {
			return nil, &Error{Code: -32002, Message: "server not initialised"}
		}
		f := sc.file(params.TextDocument.URI)
		if f == nil {
			return nil, nil
		}
		top := []map[string]any{}
		byKey := map[string]map[string]any{}
		for _, key := range sc.order {
			sym := sc.syms[key]
			if sym.file != f {
				continue
			}
			ds := map[string]any{"name": sym.name, "kind": sym.kind, "range": sym.full, "selectionRange": sym.sel, "children": []map[string]any{}}
			byKey[key] = ds
			if parent := byKey[sym.parent]; parent != nil {
				parent["children"] = append(parent["children"].([]map[string]any), ds)
			} else {
				top = append(top, ds)
			}
		}
		return top, nil
	})
}

// LSP SymbolKind values the Python scenario uses besides the sample's.
const (
	KindClass    = 5
	KindVariable = 13
)

// pyMarkdownEscaper escapes a docstring as pyright does in hover.
var pyMarkdownEscaper = strings.NewReplacer(`\`, `\\`, "_", `\_`, "*", `\*`, "[", `\[`, "]", `\]`, "<", "&lt;", ">", "&gt;")

// pythonExternals are the functions outside the workspace that the
// fixture calls, where pyright's bundled stubs declare them.
var pythonExternals = map[string]external{
	"string.capwords": {"capwords", PythonTypeshedURI + "/string/__init__.pyi", "(__init__.pyi) · Standard library",
		span{position{32, 4}, position{32, 12}}},
}

type pyScenario struct {
	mu    sync.Mutex
	files []*pyFile
	syms  map[string]*pySym
	order []string
	calls []*pyCall
	open  map[string]bool // by URI
}

type pyFile struct {
	rel, uri string
	lines    []string
	strings  [][]colSpan // per line, the spans inside string literals
}

// pySym is a declaration in the Python workspace. A symbol is found by
// decl, the start of its line (within its parent, if it has one), or by
// find, text in its parent's first line (a parameter) or body (a local
// variable or an attribute) whose first whole identifier name is the
// symbol's.
type pySym struct {
	key, name   string
	kind        int
	in          string // the file, relative to the root
	file        *pyFile
	parent      string
	decl        string
	find        string
	wide        bool   // the range is all of find (a parameter and its annotation)
	attribute   bool   // its uses are ".name"
	decorated   bool   // the declaration starts at a decorator on the line before
	typeOf      string // "float" or a symbol's key, for typeDefinition; else the symbol itself
	signature   string // as pyright hovers it at the declaration
	doc         string // the docstring, from the line after the declaration
	full, sel   span
	usesByCalls bool // a method: its uses are the calls to it
}

func (sym *pySym) callable() bool {
	return sym.kind == KindClass || sym.kind == KindFunction || sym.kind == KindMethod
}

func (sym *pySym) location(sp span) location { return location{URI: sym.file.uri, Range: sp} }

// pyItem is a call hierarchy item as pyright sends it: no data.
type pyItem struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Detail         string `json:"detail"`
	URI            string `json:"uri"`
	Range          span   `json:"range"`
	SelectionRange span   `json:"selectionRange"`
}

// item is sym's call hierarchy item; pyright adds " · Workspace" to the
// detail of an outgoing call's target.
func (sym *pySym) item(suffix string) pyItem {
	detail := "(" + path.Base(sym.file.rel) + ")"
	if sym.kind == KindMethod {
		detail = "class " + strings.SplitN(sym.key, ".", 2)[0] + " " + detail
	}
	return pyItem{Name: sym.name, Kind: sym.kind, Detail: detail + suffix, URI: sym.file.uri, Range: sym.sel, SelectionRange: sym.sel}
}

type pyCall struct {
	from, to     string // to is a symbol key or a key of pythonExternals
	name         string // the callee's name at the call site
	viaAttribute bool   // called as x.name(...)
	sites        []span
}

func pythonSymbols() []*pySym {
	shapes, report := PythonFile, PythonReportFile
	return []*pySym{
		{key: "Shape", name: "Shape", kind: KindClass, in: shapes, decl: "class Shape(", signature: "class Shape()"},
		{key: "Shape.area", name: "area", kind: KindMethod, in: shapes, parent: "Shape", decl: "    def area(", decorated: true,
			signature: "(method) def area(self: Self@Shape) -> float", usesByCalls: true},
		{key: "Rect", name: "Rect", kind: KindClass, in: shapes, decl: "class Rect(", signature: "class Rect(\n    w: float,\n    h: float\n)"},
		{key: "Rect.__init__", name: "__init__", kind: KindMethod, in: shapes, parent: "Rect", decl: "    def __init__(",
			signature: "(method) def __init__(\n    self: Self@Rect,\n    w: float,\n    h: float\n) -> None", usesByCalls: true},
		{key: "Rect.__init__.w", name: "w", kind: KindVariable, in: shapes, parent: "Rect.__init__", find: "w: float", wide: true},
		{key: "Rect.__init__.h", name: "h", kind: KindVariable, in: shapes, parent: "Rect.__init__", find: "h: float", wide: true},
		{key: "Rect.area", name: "area", kind: KindMethod, in: shapes, parent: "Rect", decl: "    def area(",
			signature: "(method) def area(self: Self@Rect) -> float", usesByCalls: true},
		{key: "Rect.w", name: "w", kind: KindVariable, in: shapes, parent: "Rect", find: "self.w =", attribute: true, typeOf: "float",
			signature: "(variable) w: float"},
		{key: "Rect.h", name: "h", kind: KindVariable, in: shapes, parent: "Rect", find: "self.h =", attribute: true, typeOf: "float",
			signature: "(variable) h: float"},
		{key: "new_rect", name: "new_rect", kind: KindFunction, in: shapes, decl: "def new_rect(",
			signature: "(function) def new_rect(\n    w: float,\n    h: float\n) -> Rect"},
		{key: "new_rect.w", name: "w", kind: KindVariable, in: shapes, parent: "new_rect", find: "w: float", wide: true},
		{key: "new_rect.h", name: "h", kind: KindVariable, in: shapes, parent: "new_rect", find: "h: float", wide: true},
		{key: "total_area", name: "total_area", kind: KindFunction, in: shapes, decl: "def total_area(",
			signature: "(function) def total_area(shapes: list[Shape]) -> float"},
		{key: "total_area.shapes", name: "shapes", kind: KindVariable, in: shapes, parent: "total_area", find: "shapes: list[Shape]", wide: true},
		{key: "total_area.total", name: "total", kind: KindVariable, in: shapes, parent: "total_area", find: "total = ", typeOf: "float"},
		{key: "total_area.s", name: "s", kind: KindVariable, in: shapes, parent: "total_area", find: "for s in", typeOf: "Shape"},
		{key: "report", name: "report", kind: KindFunction, in: shapes, decl: "def report(", signature: "(function) def report() -> float"},
		{key: "describe", name: "describe", kind: KindFunction, in: report, decl: "def describe(", signature: "(function) def describe(r: Rect) -> str"},
		{key: "describe.r", name: "r", kind: KindVariable, in: report, parent: "describe", find: "r: Rect", wide: true},
		{key: "label", name: "label", kind: KindFunction, in: report, decl: "def label(", signature: "(function) def label(s: str) -> str"},
		{key: "label.s", name: "s", kind: KindVariable, in: report, parent: "label", find: "s: str", wide: true},
	}
}

func pythonCalls() []*pyCall {
	return []*pyCall{
		{from: "new_rect", to: "Rect", name: "Rect"},
		{from: "total_area", to: "Shape.area", name: "area", viaAttribute: true},
		{from: "report", to: "total_area", name: "total_area"},
		{from: "report", to: "new_rect", name: "new_rect"},
		{from: "label", to: "string.capwords", name: "capwords", viaAttribute: true},
	}
}

func (sc *pyScenario) load(params json.RawMessage) error {
	var p struct {
		RootURI string `json:"rootUri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	dir, err := uriPath(p.RootURI)
	if err != nil {
		return fmt.Errorf("root: %w", err)
	}
	var files []*pyFile
	byRel := map[string]*pyFile{}
	for _, rel := range pythonFiles {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f := &pyFile{rel: rel, uri: (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String(),
			lines: strings.Split(string(src), "\n")}
		for _, line := range f.lines {
			f.strings = append(f.strings, stringSpans(line))
		}
		files = append(files, f)
		byRel[rel] = f
	}
	syms := map[string]*pySym{}
	var order []string
	for _, sym := range pythonSymbols() {
		sym.file = byRel[sym.in]
		if err := sym.locate(syms[sym.parent]); err != nil {
			return err
		}
		syms[sym.key] = sym
		order = append(order, sym.key)
	}
	calls := pythonCalls()
	for _, c := range calls {
		from := syms[c.from]
		for l := from.sel.Start.Line + 1; l <= from.full.End.Line; l++ {
			for _, sp := range from.file.codeIdents(l, c.name) {
				line := from.file.lines[l]
				if strings.HasPrefix(line[sp.end:], "(") && (c.viaAttribute == (sp.start > 0 && line[sp.start-1] == '.')) {
					c.sites = append(c.sites, span{position{l, sp.start}, position{l, sp.end}})
				}
			}
		}
		if len(c.sites) == 0 {
			return fmt.Errorf("lspfake: no call site for %s -> %s", c.from, c.to)
		}
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.files, sc.syms, sc.order, sc.calls, sc.open = files, syms, order, calls, map[string]bool{}
	return nil
}

// locate fills the symbol's ranges and doc from its file. The fixture is
// ASCII, so byte offsets equal UTF-16 offsets.
func (sym *pySym) locate(parent *pySym) error {
	lines := sym.file.lines
	from, to := 0, len(lines)-1
	if parent != nil {
		from, to = parent.full.Start.Line, parent.full.End.Line
	}
	if sym.find != "" {
		if parent.kind != KindClass {
			to = parent.full.End.Line
			if sym.wide {
				to = parent.sel.Start.Line // a parameter: on the def line
			}
		}
		for i := max(from, parent.sel.Start.Line); i <= to; i++ {
			col := strings.Index(lines[i], sym.find)
			if col < 0 {
				continue
			}
			name := col + identSpans(sym.find, sym.name)[0].start
			sym.sel = span{position{i, name}, position{i, name + len(sym.name)}}
			sym.full = sym.sel
			if sym.wide {
				sym.full = span{position{i, col}, position{i, col + len(sym.find)}}
				sym.sel = sym.full
			}
			return nil
		}
		return fmt.Errorf("lspfake: %q not found for %s", sym.find, sym.key)
	}
	for i := from; i <= to; i++ {
		if !strings.HasPrefix(lines[i], sym.decl) {
			continue
		}
		indent := len(lines[i]) - len(strings.TrimLeft(lines[i], " "))
		nameCol := indent + strings.Index(lines[i][indent:], sym.name)
		sym.sel = span{position{i, nameCol}, position{i, nameCol + len(sym.name)}}
		end := i
		for j := i + 1; j < len(lines); j++ {
			if t := strings.TrimSpace(lines[j]); t == "" {
				continue
			} else if len(lines[j])-len(strings.TrimLeft(lines[j], " ")) <= indent {
				break
			}
			end = j
		}
		start := position{i, indent}
		if sym.decorated {
			start = position{i - 1, indent}
		}
		sym.full = span{start, position{end, len(lines[end])}}
		if i+1 < len(lines) {
			if doc, ok := strings.CutPrefix(strings.TrimSpace(lines[i+1]), `"""`); ok {
				sym.doc = strings.TrimSuffix(doc, `"""`)
			}
		}
		return nil
	}
	return fmt.Errorf("lspfake: declaration %q not found", sym.decl)
}

// stringSpans returns where the string literals of a line are. The fixture
// only has strings that open and close on one line.
func stringSpans(line string) []colSpan {
	var out []colSpan
	for i := 0; i < len(line); i++ {
		if line[i] == '#' {
			out = append(out, colSpan{i, len(line)})
			break
		}
		if line[i] != '"' && line[i] != '\'' {
			continue
		}
		quote := line[i : i+1]
		if strings.HasPrefix(line[i:], strings.Repeat(quote, 3)) {
			quote = strings.Repeat(quote, 3)
		}
		end := strings.Index(line[i+len(quote):], quote)
		if end < 0 {
			out = append(out, colSpan{i, len(line)})
			break
		}
		out = append(out, colSpan{i, i + len(quote) + end + len(quote)})
		i += len(quote) + end + len(quote) - 1
	}
	return out
}

// codeIdents returns where name occurs as a whole identifier on line l,
// outside strings and comments.
func (f *pyFile) codeIdents(l int, name string) []colSpan {
	var out []colSpan
	for _, sp := range identSpans(f.lines[l], name) {
		inString := false
		for _, s := range f.strings[l] {
			if sp.start >= s.start && sp.end <= s.end {
				inString = true
			}
		}
		if !inString {
			out = append(out, sp)
		}
	}
	return out
}

// uses returns the uses of sym in every file, in file order: the call
// sites of a method, the ".name" of an attribute, the name of a class or
// function. Parameters and local variables have none here.
func (sc *pyScenario) uses(sym *pySym) []location {
	var out []location
	if sym.usesByCalls {
		for _, c := range sc.calls {
			if c.to == sym.key {
				for _, site := range c.sites {
					out = append(out, sc.syms[c.from].location(site))
				}
			}
		}
		return out
	}
	if sym.kind == KindVariable && !sym.attribute {
		return nil
	}
	for _, f := range sc.files {
		for l := range f.lines {
			for _, sp := range f.codeIdents(l, sym.name) {
				if f == sym.file && l == sym.sel.Start.Line && sp.start == sym.sel.Start.Character {
					continue // the declaration
				}
				dotted := sp.start > 0 && f.lines[l][sp.start-1] == '.'
				if dotted != sym.attribute {
					continue
				}
				out = append(out, location{URI: f.uri, Range: span{position{l, sp.start}, position{l, sp.end}}})
			}
		}
	}
	return out
}

func (sc *pyScenario) file(uri string) *pyFile {
	for _, f := range sc.files {
		if f.uri == uri {
			return f
		}
	}
	return nil
}

// at returns the symbol at pos in an open file: the callee at a call
// site, the symbol at its declaration's name or at one of its uses.
func (sc *pyScenario) at(uri string, pos position) *pySym {
	if !sc.open[uri] || sc.file(uri) == nil {
		return nil
	}
	for _, c := range sc.calls {
		for _, site := range c.sites {
			if sc.syms[c.from].file.uri == uri && site.contains(pos) {
				return sc.syms[c.to] // nil for an external callee: not modelled
			}
		}
	}
	for _, key := range sc.order {
		sym := sc.syms[key]
		name := sym.sel
		if sym.wide { // a parameter's annotation is about its type, not about it
			name.End = position{name.Start.Line, name.Start.Character + len(sym.name)}
		}
		if sym.file.uri == uri && name.contains(pos) {
			return sym
		}
	}
	for _, key := range sc.order {
		for _, use := range sc.uses(sc.syms[key]) {
			if use.URI == uri && use.Range.contains(pos) {
				return sc.syms[key]
			}
		}
	}
	return nil
}

func (sc *pyScenario) opening(open bool) Handler {
	return func(_ context.Context, p json.RawMessage) (any, error) {
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(p, &params)
		sc.mu.Lock()
		defer sc.mu.Unlock()
		if sc.open != nil {
			if open {
				sc.open[params.TextDocument.URI] = true
			} else {
				delete(sc.open, params.TextDocument.URI)
			}
		}
		return nil, nil
	}
}

// positional adapts f to a textDocument/* request on an open file. It
// answers null when no symbol is at the position.
func (sc *pyScenario) positional(f func(*pySym, position, json.RawMessage) (any, error)) Handler {
	return func(_ context.Context, p json.RawMessage) (any, error) {
		var params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Position position `json:"position"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
		}
		sc.mu.Lock()
		defer sc.mu.Unlock()
		if sc.syms == nil {
			return nil, &Error{Code: -32002, Message: "server not initialised"}
		}
		sym := sc.at(params.TextDocument.URI, params.Position)
		if sym == nil {
			return nil, nil
		}
		return f(sym, params.Position, p)
	}
}

// hierarchy adapts f to a callHierarchy/* request. pyright sends no data
// with its items, so the item is found by where its name is.
func (sc *pyScenario) hierarchy(f func(*pySym) any) Handler {
	return func(_ context.Context, p json.RawMessage) (any, error) {
		var params struct {
			Item pyItem `json:"item"`
		}
		if err := json.Unmarshal(p, &params); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
		}
		sc.mu.Lock()
		defer sc.mu.Unlock()
		for _, key := range sc.order {
			if sym := sc.syms[key]; sym.file.uri == params.Item.URI && sym.sel == params.Item.SelectionRange && sym.callable() {
				return f(sym), nil
			}
		}
		return nil, nil
	}
}
