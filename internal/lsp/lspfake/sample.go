package lspfake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// SampleFile is the main file of the sample workspace (testdata/sample),
// in package example.com/sample.
const SampleFile = "shapes.go"

// SampleReportFile is the file of package example.com/sample/report, which
// imports the sample package, fmt and strings. The path is relative to
// the root, with slashes.
const SampleReportFile = "report/report.go"

// sampleFiles are the files of the sample workspace, in order.
var sampleFiles = []string{SampleFile, SampleReportFile}

// SampleBuiltinURI is where the scenario says float64 and string are
// declared, as gopls points into $GOROOT/src/builtin. It is outside the
// workspace and does not exist, so a client that tries to read it fails.
const SampleBuiltinURI = "file:///lspfake-goroot/src/builtin/builtin.go"

// builtins are the names of the predeclared types in SampleBuiltinURI.
var builtins = map[string]span{
	"float64": {position{59, 5}, position{59, 12}},
	"string":  {position{72, 5}, position{72, 11}},
}

// external is a function outside the workspace that the sample calls, as
// gopls reports it from $GOROOT.
type external struct {
	name, uri, detail string
	sel               span
}

var sampleExternals = map[string]external{
	"fmt.Sprintf": {"Sprintf", "file:///lspfake-goroot/src/fmt/print.go", "fmt • print.go",
		span{position{236, 5}, position{236, 12}}},
	"strings.ToUpper": {"ToUpper", "file:///lspfake-goroot/src/strings/strings.go", "strings • strings.go",
		span{position{686, 5}, position{686, 12}}},
}

// errNotApplicable is how gopls (code 0) refuses a request that does not
// apply to the symbol, e.g. call hierarchy on a type.
func errNotApplicable(msg string) error { return &Error{Code: 0, Message: msg} }

// LSP SymbolKind values used by the sample.
const (
	KindMethod    = 6
	KindField     = 8
	KindInterface = 11
	KindFunction  = 12
	KindStruct    = 23
)

// EnvHelper names the environment variable that turns a test binary into a
// fake language server. See RunHelperIfRequested.
const EnvHelper = "EGO_DIR_LSPFAKE"

// RunHelperIfRequested serves the Sample scenario on stdin/stdout and exits
// when EnvHelper is set to "sample". Call it first in TestMain. A test can
// then start os.Args[0] (with the variable set) as a real language-server
// process without needing gopls installed.
func RunHelperIfRequested() {
	if os.Getenv(EnvHelper) != "sample" {
		return
	}
	s := New()
	Sample(s)
	err := s.Serve(context.Background(), struct {
		io.Reader
		io.Writer
	}{os.Stdin, os.Stdout})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lspfake:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// SampleRoot returns the absolute path of testdata/sample, walking up from
// the current directory (go test runs in the package directory).
func SampleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(dir, "testdata", "sample")
		if _, err := os.Stat(filepath.Join(p, SampleFile)); err == nil {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("lspfake: testdata/sample not found")
		}
		dir = parent
	}
}

// Sample scripts s to answer like gopls for the sample workspace.
//
// The workspace root is taken from the initialise request (rootUri, or the
// first workspace folder), and every position is computed from the files
// on disk, so the responses cannot drift from the fixture.
//
// Call hierarchy items carry a "data" field and incoming/outgoing calls fail
// with InvalidParams when it is not sent back unchanged, as gopls requires.
//
// Like gopls (v0.23), it also:
//   - names concrete methods "(T).M" in the outline, and starts the range of
//     a type or interface method at its name;
//   - lists struct fields as children of their struct, all fields declared
//     together ("W, H float64") sharing one range;
//   - gives call hierarchy items kind Function and the name as their range;
//   - answers prepareCallHierarchy on a type, field or package name, and
//     implementation on a function, field or package name, with an error;
//   - lists TotalArea as a caller of Rect.Area (dynamic dispatch through
//     Shape), but not Rect.Area as a callee of TotalArea;
//   - answers typeDefinition of a function or field with its type, which
//     for float64 and string is SampleBuiltinURI;
//   - reports calls into the standard library at $GOROOT-like URIs;
//   - answers definition on the name in a package clause with that name,
//     and references with the import specs that import the package;
//   - formats hover with rules, a pkg.go.dev link, a layout comment on the
//     declaration of a type or field and the method set of a struct, and
//     hover on a package name with its path, module and Go version.
//
// It does not answer requests on the package qualifiers in selectors
// ("fmt" in fmt.Sprintf).
func Sample(s *Server) {
	sc := &sampleScenario{}
	initialise := func(_ context.Context, p json.RawMessage) (any, error) {
		if err := sc.load(p); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
		}
		return map[string]any{
			"capabilities": map[string]any{
				"definitionProvider":     true,
				"referencesProvider":     true,
				"typeDefinitionProvider": true,
				"implementationProvider": true,
				"callHierarchyProvider":  true,
				"hoverProvider":          true,
			},
			"serverInfo": map[string]any{"name": "lspfake"},
		}, nil
	}
	// Answer both spellings, so the scenario works whichever the client sends.
	s.Handle("initialise", initialise)
	s.Handle("initialize", initialise)
	s.Handle("textDocument/definition", sc.positional(func(sym *symbol, _ position, _ json.RawMessage) (any, error) {
		return sym.location(sym.sel), nil
	}))
	s.Handle("textDocument/typeDefinition", sc.positional(func(sym *symbol, _ position, _ json.RawMessage) (any, error) {
		if sym.header {
			return nil, errNotApplicable("no enclosing expression has a type")
		}
		if sp, ok := builtins[sym.typeOf]; ok {
			return []location{{URI: SampleBuiltinURI, Range: sp}}, nil
		}
		switch {
		case sym.typeOf != "":
			t := sc.syms[sym.typeOf]
			return []location{t.location(t.sel)}, nil
		case sym.isType():
			return []location{sym.location(sym.sel)}, nil
		}
		return nil, nil
	}))
	s.Handle("textDocument/implementation", sc.positional(func(sym *symbol, _ position, _ json.RawMessage) (any, error) {
		switch {
		case sym.header:
			return nil, errNotApplicable(sym.name + " denotes unknown object")
		case sym.kind == KindFunction:
			return nil, errNotApplicable(sym.name + " is a function, not a method (query at 'func' token to find matching signatures)")
		case sym.kind == KindField:
			return nil, errNotApplicable(sym.name + " is a var, not a type")
		}
		out := []location{}
		for _, key := range sc.implementations(sym.key) {
			t := sc.syms[key]
			out = append(out, t.location(t.sel))
		}
		return out, nil
	}))
	s.Handle("textDocument/references", sc.positional(func(sym *symbol, _ position, p json.RawMessage) (any, error) {
		var params struct {
			Context struct {
				IncludeDeclaration bool `json:"includeDeclaration"`
			} `json:"context"`
		}
		_ = json.Unmarshal(p, &params)
		var out []location
		if params.Context.IncludeDeclaration {
			out = append(out, sym.location(sym.sel))
		}
		for _, c := range sc.calls {
			if c.to == sym.key || c.dynamic == sym.key {
				from := sc.syms[c.from]
				for _, site := range c.sites {
					out = append(out, from.location(site))
				}
			}
		}
		out = append(out, sym.uses...)
		sortLocations(out)
		if out == nil {
			out = []location{}
		}
		return out, nil
	}))
	s.Handle("textDocument/hover", sc.positional(func(sym *symbol, pos position, _ json.RawMessage) (any, error) {
		return map[string]any{
			"contents": map[string]any{"kind": "markdown", "value": sc.hover(sym, pos)},
			"range":    sym.sel,
		}, nil
	}))
	s.Handle("textDocument/prepareCallHierarchy", sc.positional(func(sym *symbol, _ position, _ json.RawMessage) (any, error) {
		switch {
		case sym.header:
			return nil, errNotApplicable("no symbol here")
		case sym.kind != KindFunction && sym.kind != KindMethod:
			return nil, errNotApplicable(sym.name + " is not a function")
		}
		return []callHierarchyItem{sym.item()}, nil
	}))
	s.Handle("callHierarchy/incomingCalls", sc.hierarchy(func(sym *symbol) any {
		out := []map[string]any{}
		for _, c := range sc.calls {
			if c.to == sym.key || c.dynamic == sym.key {
				out = append(out, map[string]any{"from": sc.syms[c.from].item(), "fromRanges": c.sites})
			}
		}
		return out
	}))
	s.Handle("callHierarchy/outgoingCalls", sc.hierarchy(func(sym *symbol) any {
		out := []map[string]any{}
		for _, c := range sc.calls {
			if c.from != sym.key {
				continue
			}
			to := map[string]any{"to": nil, "fromRanges": c.sites}
			if ext, ok := sampleExternals[c.to]; ok {
				to["to"] = ext.item(c.to)
			} else {
				to["to"] = sc.syms[c.to].item()
			}
			out = append(out, to)
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
		// Hierarchical, as gopls answers: interface methods and struct
		// fields are children of their type; everything else is top level.
		var top []map[string]any
		byKey := map[string]map[string]any{}
		for _, key := range sc.order {
			sym := sc.syms[key]
			if sym.file != f {
				continue
			}
			ds := map[string]any{
				"name": sym.outlineName(), "kind": sym.kind,
				"range": sym.full, "selectionRange": sym.sel,
			}
			byKey[key] = ds
			if parent := byKey[sym.parent]; parent != nil {
				children, _ := parent["children"].([]map[string]any)
				parent["children"] = append(children, ds)
			} else {
				top = append(top, ds)
			}
		}
		return top, nil
	})
	for _, n := range []string{"textDocument/didOpen", "textDocument/didClose", "initialised", "initialized"} {
		s.Handle(n, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	}
}

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type span struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

func (sp span) contains(p position) bool {
	after := p.Line > sp.Start.Line || (p.Line == sp.Start.Line && p.Character >= sp.Start.Character)
	before := p.Line < sp.End.Line || (p.Line == sp.End.Line && p.Character < sp.End.Character)
	return after && before
}

type location struct {
	URI   string `json:"uri"`
	Range span   `json:"range"`
}

// sortLocations orders locations by URI, then position, as gopls does.
func sortLocations(locs []location) {
	sort.Slice(locs, func(i, j int) bool {
		a, b := locs[i], locs[j]
		if a.URI != b.URI {
			return a.URI < b.URI
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line < b.Range.Start.Line
		}
		return a.Range.Start.Character < b.Range.Start.Character
	})
}

type callHierarchyItem struct {
	Name           string          `json:"name"`
	Kind           int             `json:"kind"`
	Detail         string          `json:"detail"`
	URI            string          `json:"uri"`
	Range          span            `json:"range"`
	SelectionRange span            `json:"selectionRange"`
	Data           json.RawMessage `json:"data"`
}

func itemData(key string) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"key": key})
	return data
}

func (ext external) item(key string) callHierarchyItem {
	return callHierarchyItem{Name: ext.name, Kind: KindFunction, Detail: ext.detail, URI: ext.uri,
		Range: ext.sel, SelectionRange: ext.sel, Data: itemData(key)}
}

// sampleFile is one file of the sample workspace and its package.
type sampleFile struct {
	rel       string // relative to the root, with slashes
	path, uri string
	lines     []string
	pkgPath   string // import path
	pkgName   string
	pkgDoc    string  // the package doc comment, without "// "
	header    *symbol // the name in the package clause
}

type symbol struct {
	key       string // unique, e.g. "Rect.Area"
	name      string
	kind      int
	in        string // the file, relative to the root
	file      *sampleFile
	parent    string // key of the type it is nested in, in the outline
	decl      string // unique line prefix that declares the symbol (not fields)
	typeOf    string // key of the symbol's type, or a builtin type
	full, sel span
	signature string // as gopls hovers it; derived from the source if empty
	doc       string
	linkName  string     // the pkg.go.dev link text
	methods   string     // a struct's method set, as gopls hovers it
	uses      []location // a type's or field's uses, for references and definition
	header    bool       // the name in a package clause
}

func (sym *symbol) isType() bool { return sym.kind == KindInterface || sym.kind == KindStruct }

func (sym *symbol) location(sp span) location { return location{URI: sym.file.uri, Range: sp} }

func (sym *symbol) item() callHierarchyItem {
	return callHierarchyItem{
		Name:           sym.name,
		Detail:         sym.file.pkgPath + " • " + path.Base(sym.file.rel),
		URI:            sym.file.uri,
		Kind:           KindFunction,
		Range:          sym.sel,
		SelectionRange: sym.sel,
		Data:           itemData(sym.key),
	}
}

// outlineName is the documentSymbol name: gopls writes "(T).M" for a
// concrete method, and plain "M" for an interface method.
func (sym *symbol) outlineName() string {
	if recv, _, ok := strings.Cut(sym.key, "."); ok && sym.kind == KindMethod && strings.HasPrefix(sym.decl, "func (") {
		return "(" + recv + ")." + sym.name
	}
	return sym.name
}

type call struct {
	from, to string // to is a symbol key or a key of sampleExternals
	needle   string
	sites    []span // in the file of from
	dynamic  string // a concrete method the call reaches through the interface
}

type sampleScenario struct {
	mu        sync.Mutex
	modPath   string
	goVersion string // from go.mod, e.g. "1.26"
	files     []*sampleFile
	syms      map[string]*symbol
	order     []string
	calls     []*call
}

// sampleSymbols describes every declaration in the sample, in file order.
// Fields come after their struct.
func sampleSymbols() []*symbol {
	return []*symbol{
		{key: "Shape", name: "Shape", kind: KindInterface, in: SampleFile, decl: "type Shape interface"},
		{key: "Shape.Area", name: "Area", kind: KindMethod, in: SampleFile, parent: "Shape", decl: "\tArea() float64", typeOf: "float64",
			signature: "func (Shape) Area() float64", linkName: "(sample.Shape).Area"},
		{key: "Rect", name: "Rect", kind: KindStruct, in: SampleFile, decl: "type Rect struct", methods: "func (r Rect) Area() float64"},
		{key: "Rect.W", name: "W", kind: KindField, in: SampleFile, parent: "Rect", typeOf: "float64",
			signature: "field W float64 // size=8, offset=0", linkName: "(sample.Rect).W"},
		{key: "Rect.H", name: "H", kind: KindField, in: SampleFile, parent: "Rect", typeOf: "float64",
			signature: "field H float64 // size=8, offset=8", linkName: "(sample.Rect).H"},
		{key: "Rect.Area", name: "Area", kind: KindMethod, in: SampleFile, decl: "func (r Rect) Area()", typeOf: "float64",
			linkName: "(sample.Rect).Area"},
		{key: "NewRect", name: "NewRect", kind: KindFunction, in: SampleFile, decl: "func NewRect(", typeOf: "Rect",
			signature: "func NewRect(w float64, h float64) Rect"},
		{key: "TotalArea", name: "TotalArea", kind: KindFunction, in: SampleFile, decl: "func TotalArea(", typeOf: "float64"},
		{key: "Report", name: "Report", kind: KindFunction, in: SampleFile, decl: "func Report(", typeOf: "float64"},
		{key: "Describe", name: "Describe", kind: KindFunction, in: SampleReportFile, decl: "func Describe(", typeOf: "string"},
		{key: "Label", name: "Label", kind: KindFunction, in: SampleReportFile, decl: "func Label(", typeOf: "string"},
	}
}

func sampleCalls() []*call {
	return []*call{
		{from: "TotalArea", to: "Shape.Area", needle: "Area(", dynamic: "Rect.Area"},
		{from: "Report", to: "TotalArea", needle: "TotalArea("},
		{from: "Report", to: "NewRect", needle: "NewRect("},
		{from: "Describe", to: "fmt.Sprintf", needle: "Sprintf("},
		{from: "Label", to: "strings.ToUpper", needle: "ToUpper("},
	}
}

// implementations maps each type or method to its counterparts, in both
// directions, as gopls does.
func (sc *sampleScenario) implementations(key string) []string {
	pairs := [][2]string{{"Rect", "Shape"}, {"Rect.Area", "Shape.Area"}}
	var out []string
	for _, p := range pairs {
		switch key {
		case p[0]:
			out = append(out, p[1])
		case p[1]:
			out = append(out, p[0])
		}
	}
	return out
}

func (sc *sampleScenario) load(params json.RawMessage) error {
	var p struct {
		RootURI          string `json:"rootUri"`
		WorkspaceFolders []struct {
			URI string `json:"uri"`
		} `json:"workspaceFolders"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	root := p.RootURI
	if root == "" && len(p.WorkspaceFolders) > 0 {
		root = p.WorkspaceFolders[0].URI
	}
	dir, err := uriPath(root)
	if err != nil {
		return fmt.Errorf("root: %w", err)
	}
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return err
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.modPath, sc.goVersion = "", ""
	for _, line := range strings.Split(string(gomod), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
			sc.modPath = f[1]
		} else if len(f) == 2 && f[0] == "go" {
			sc.goVersion = f[1]
		}
	}
	sc.files = nil
	byRel := map[string]*sampleFile{}
	for _, rel := range sampleFiles {
		f, err := sc.loadFile(dir, rel)
		if err != nil {
			return err
		}
		sc.files = append(sc.files, f)
		byRel[rel] = f
	}
	sc.syms = map[string]*symbol{}
	sc.order = nil
	for _, sym := range sampleSymbols() {
		sym.file = byRel[sym.in]
		var err error
		if sym.kind == KindField {
			err = sym.locateField(sc.syms[sym.parent])
		} else {
			err = sym.locate()
		}
		if err != nil {
			return err
		}
		sc.syms[sym.key] = sym
		sc.order = append(sc.order, sym.key)
	}
	for _, key := range sc.order {
		if sym := sc.syms[key]; sym.isType() || sym.kind == KindField {
			sym.uses = sc.identUses(sym)
		}
	}
	for _, f := range sc.files {
		f.header.uses = sc.importSpecs(f.pkgPath)
	}
	sc.calls = sampleCalls()
	for _, c := range sc.calls {
		from := sc.syms[c.from]
		lines := from.file.lines
		for l := from.full.Start.Line; l <= from.full.End.Line; l++ {
			line := lines[l]
			off := 0
			if l == from.sel.Start.Line {
				off = from.sel.End.Character // not the declaration's own name
			}
			for {
				i := strings.Index(line[off:], c.needle)
				if i < 0 {
					break
				}
				col := off + i
				name := strings.TrimSuffix(c.needle, "(")
				c.sites = append(c.sites, span{position{l, col}, position{l, col + len(name)}})
				off = col + len(c.needle)
			}
		}
		if len(c.sites) == 0 {
			return fmt.Errorf("no call site for %s -> %s", c.from, c.to)
		}
	}
	return nil
}

// loadFile reads one file of the workspace and its package clause.
func (sc *sampleScenario) loadFile(root, rel string) (*sampleFile, error) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	src, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	f := &sampleFile{
		rel:   rel,
		path:  p,
		uri:   (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String(),
		lines: strings.Split(string(src), "\n"),
	}
	f.pkgPath = sc.modPath
	if d := path.Dir(rel); d != "." {
		f.pkgPath += "/" + d
	}
	for i, line := range f.lines {
		name, ok := strings.CutPrefix(line, "package ")
		if !ok {
			continue
		}
		f.pkgName = name
		if i > 0 {
			f.pkgDoc, _ = strings.CutPrefix(f.lines[i-1], "// ")
		}
		f.header = &symbol{key: "package " + f.pkgPath, name: name, file: f, header: true,
			sel: span{position{i, len("package ")}, position{i, len("package ") + len(name)}}}
		f.header.full = f.header.sel
		return f, nil
	}
	return nil, fmt.Errorf("lspfake: no package clause in %s", rel)
}

// locate fills the symbol's ranges, signature and doc from its file.
// The fixture is ASCII, so byte offsets equal UTF-16 offsets.
func (sym *symbol) locate() error {
	lines := sym.file.lines
	for i, line := range lines {
		if !strings.HasPrefix(line, sym.decl) {
			continue
		}
		nameCol := strings.Index(line, sym.name)
		if strings.HasPrefix(sym.decl, "func (") {
			nameCol = strings.Index(line, ") "+sym.name) + 2
		}
		end := i
		if strings.HasSuffix(line, "{") {
			for end < len(lines) && lines[end] != "}" {
				end++
			}
		}
		// gopls starts a func's range at "func", and anything else's at its name.
		startCol := 0
		if !strings.HasPrefix(sym.decl, "func ") {
			startCol = nameCol
		}
		sym.full = span{position{i, startCol}, position{end, len(lines[end])}}
		sym.sel = span{position{i, nameCol}, position{i, nameCol + len(sym.name)}}
		switch {
		case sym.signature != "":
		case sym.isType():
			// The whole declaration, with the size gopls adds on 64-bit platforms.
			sym.signature = line + " // size=16 (0x10)"
			if end > i {
				sym.signature += "\n" + strings.Join(lines[i+1:end+1], "\n")
			}
		default:
			sym.signature, _, _ = strings.Cut(strings.TrimSpace(line), " {")
		}
		if sym.linkName == "" {
			sym.linkName = sym.file.pkgName + "." + sym.key
		}
		if i > 0 {
			if doc, ok := strings.CutPrefix(strings.TrimSpace(lines[i-1]), "// "); ok {
				sym.doc = doc
			}
		}
		return nil
	}
	return fmt.Errorf("lspfake: declaration %q not found", sym.decl)
}

// locateField fills a field's ranges from its struct's one-line
// declaration, "type T struct{ A, B float64 }". As with gopls, the range of
// each field is the whole field list.
func (sym *symbol) locateField(parent *symbol) error {
	i := parent.sel.Start.Line
	line := sym.file.lines[i]
	open, closing := strings.Index(line, "{")+1, strings.LastIndex(line, "}")
	if open == 0 || closing < open {
		return fmt.Errorf("lspfake: struct %s is not on one line", parent.name)
	}
	list := line[open:closing]
	start := open + len(list) - len(strings.TrimLeft(list, " "))
	end := open + len(strings.TrimRight(list, " "))
	for _, sp := range identSpans(line, sym.name) {
		if sp.start >= start && sp.end <= end {
			sym.full = span{position{i, start}, position{i, end}}
			sym.sel = span{position{i, sp.start}, position{i, sp.end}}
			return nil
		}
	}
	return fmt.Errorf("lspfake: field %s not found in %s", sym.name, parent.name)
}

type colSpan struct{ start, end int }

// identSpans returns where name occurs in line as a whole identifier.
func identSpans(line, name string) []colSpan {
	isIdent := func(b byte) bool {
		return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
	}
	var out []colSpan
	for off := 0; ; {
		i := strings.Index(line[off:], name)
		if i < 0 {
			return out
		}
		col, end := off+i, off+i+len(name)
		if (col == 0 || !isIdent(line[col-1])) && (end == len(line) || !isIdent(line[end])) {
			out = append(out, colSpan{col, end})
		}
		off = end
	}
}

// identUses finds the uses of a type or field in every file: its name as a
// whole identifier, outside comments and the line that declares it.
func (sc *sampleScenario) identUses(sym *symbol) []location {
	var out []location
	for _, f := range sc.files {
		for l, line := range f.lines {
			if f == sym.file && l == sym.sel.Start.Line || strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, sp := range identSpans(line, sym.name) {
				out = append(out, location{URI: f.uri, Range: span{position{l, sp.start}, position{l, sp.end}}})
			}
		}
	}
	return out
}

// importSpecs finds the import specs, on lines of their own, that import
// pkgPath: gopls answers references to a package name with them.
func (sc *sampleScenario) importSpecs(pkgPath string) []location {
	quoted := strconv.Quote(pkgPath)
	var out []location
	for _, f := range sc.files {
		for l, line := range f.lines {
			if strings.TrimSpace(line) == quoted {
				col := strings.Index(line, quoted)
				out = append(out, location{URI: f.uri, Range: span{position{l, col}, position{l, col + len(quoted)}}})
			}
		}
	}
	return out
}

// hover is the hover Markdown for sym, asked at pos.
func (sc *sampleScenario) hover(sym *symbol, pos position) string {
	if sym.header {
		f := sym.file
		value := "```go\npackage " + f.pkgName + "\n```\n\n---\n\n"
		if f.pkgDoc != "" {
			value += f.pkgDoc + "\n\n\n---\n\n"
		}
		return value + " - Package path: " + f.pkgPath + "\n - Module: " + sc.modPath + "\n - Language version: go" + sc.goVersion
	}
	signature := sym.signature
	if sym.kind == KindField && !sym.sel.contains(pos) {
		// At a use, gopls leaves out the field's layout.
		signature, _, _ = strings.Cut(signature, " //")
	}
	value := "```go\n" + signature + "\n```\n\n---\n\n"
	if sym.doc != "" {
		value += sym.doc + "\n\n\n"
	}
	if sym.methods != "" {
		value += "```go\n" + sym.methods + "\n```\n\n---\n\n"
	} else if sym.doc != "" {
		value += "---\n\n"
	}
	return value + "[`" + sym.linkName + "` on pkg.go.dev](https://pkg.go.dev/" + sym.file.pkgPath + "#" + sym.key + ")"
}

// file returns the workspace file at uri, or nil.
func (sc *sampleScenario) file(uri string) *sampleFile {
	p, err := uriPath(uri)
	if err != nil {
		return nil
	}
	for _, f := range sc.files {
		if f.path == p {
			return f
		}
	}
	return nil
}

// at returns the symbol at pos: the callee when pos is on a call site, the
// declared symbol when pos is on a declaration's name or a use of a type
// or field, the package when pos is on the name in the package clause,
// otherwise nil.
func (sc *sampleScenario) at(uri string, pos position) *symbol {
	f := sc.file(uri)
	if f == nil {
		return nil
	}
	for _, c := range sc.calls {
		if sc.syms[c.from].file != f {
			continue
		}
		for _, site := range c.sites {
			if site.contains(pos) {
				if _, ok := sampleExternals[c.to]; ok {
					return nil // not modelled
				}
				return sc.syms[c.to]
			}
		}
	}
	if f.header.sel.contains(pos) {
		return f.header
	}
	for _, key := range sc.order {
		sym := sc.syms[key]
		if sym.file == f && sym.sel.contains(pos) {
			return sym
		}
		for _, use := range sym.uses {
			if use.URI == f.uri && use.Range.contains(pos) {
				return sym
			}
		}
	}
	return nil
}

// positional adapts f to a textDocument/* request. It answers null when no
// symbol is at the requested position.
func (sc *sampleScenario) positional(f func(*symbol, position, json.RawMessage) (any, error)) Handler {
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

// hierarchy adapts f to a callHierarchy/* request, resolving the item by the
// data field the server attached in prepareCallHierarchy.
func (sc *sampleScenario) hierarchy(f func(*symbol) any) Handler {
	return func(_ context.Context, p json.RawMessage) (any, error) {
		var params struct {
			Item struct {
				Data json.RawMessage `json:"data"`
			} `json:"item"`
		}
		_ = json.Unmarshal(p, &params)
		var data struct {
			Key string `json:"key"`
		}
		if len(params.Item.Data) == 0 || json.Unmarshal(params.Item.Data, &data) != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: "call hierarchy item data was not sent back"}
		}
		sc.mu.Lock()
		defer sc.mu.Unlock()
		sym := sc.syms[data.Key]
		if sym == nil {
			return nil, &Error{Code: CodeInvalidParams, Message: "unknown call hierarchy item " + data.Key}
		}
		return f(sym), nil
	}
}

func uriPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("not a file URI: %q", uri)
	}
	return filepath.Clean(filepath.FromSlash(u.Path)), nil
}
