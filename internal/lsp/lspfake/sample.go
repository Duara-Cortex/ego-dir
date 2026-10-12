package lspfake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SampleFile is the only file of the sample workspace (testdata/sample).
const SampleFile = "shapes.go"

// SampleBuiltinURI is where the scenario says float64 is declared, as gopls
// points into $GOROOT/src/builtin. It is outside the workspace and does not
// exist, so a client that tries to read it fails.
const SampleBuiltinURI = "file:///lspfake-goroot/src/builtin/builtin.go"

// builtinFloat64 is the name of float64 in SampleBuiltinURI.
var builtinFloat64 = span{position{59, 5}, position{59, 12}}

// errNotApplicable is how gopls (code 0) refuses a request that does not
// apply to the symbol, e.g. call hierarchy on a type.
func errNotApplicable(msg string) error { return &Error{Code: 0, Message: msg} }

// LSP SymbolKind values used by the sample.
const (
	KindMethod    = 6
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
// first workspace folder), and every position is computed from shapes.go on
// disk, so the responses cannot drift from the fixture.
//
// Call hierarchy items carry a "data" field and incoming/outgoing calls fail
// with InvalidParams when it is not sent back unchanged, as gopls requires.
//
// Like gopls (v0.23), it also:
//   - names concrete methods "(T).M" in the outline, and starts the range of
//     a type or interface method at its name;
//   - gives call hierarchy items kind Function and the name as their range;
//   - answers prepareCallHierarchy on a type, and implementation on a
//     function, with an error;
//   - lists TotalArea as a caller of Rect.Area (dynamic dispatch through
//     Shape), but not Rect.Area as a callee of TotalArea;
//   - answers typeDefinition of a function with its result type, which for
//     float64 is SampleBuiltinURI;
//   - formats hover with rules, a pkg.go.dev link, a size comment on types
//     and the method set of a struct.
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
	s.Handle("textDocument/definition", sc.positional(func(sym *symbol, _ json.RawMessage) (any, error) {
		return sc.location(sym.sel), nil
	}))
	s.Handle("textDocument/typeDefinition", sc.positional(func(sym *symbol, _ json.RawMessage) (any, error) {
		switch {
		case sym.typeOf == "float64":
			return []location{{URI: SampleBuiltinURI, Range: builtinFloat64}}, nil
		case sym.typeOf != "":
			return []location{sc.location(sc.syms[sym.typeOf].sel)}, nil
		case sym.isType():
			return []location{sc.location(sym.sel)}, nil
		}
		return nil, nil
	}))
	s.Handle("textDocument/implementation", sc.positional(func(sym *symbol, _ json.RawMessage) (any, error) {
		if sym.kind == KindFunction {
			return nil, errNotApplicable(sym.name + " is a function, not a method (query at 'func' token to find matching signatures)")
		}
		out := []location{}
		for _, key := range sc.implementations(sym.key) {
			out = append(out, sc.location(sc.syms[key].sel))
		}
		return out, nil
	}))
	s.Handle("textDocument/references", sc.positional(func(sym *symbol, p json.RawMessage) (any, error) {
		var params struct {
			Context struct {
				IncludeDeclaration bool `json:"includeDeclaration"`
			} `json:"context"`
		}
		_ = json.Unmarshal(p, &params)
		var spans []span
		if params.Context.IncludeDeclaration {
			spans = append(spans, sym.sel)
		}
		for _, c := range sc.calls {
			if c.to == sym.key || c.dynamic == sym.key {
				spans = append(spans, c.sites...)
			}
		}
		spans = append(spans, sym.uses...)
		sortSpans(spans)
		out := []location{}
		for _, sp := range spans {
			out = append(out, sc.location(sp))
		}
		return out, nil
	}))
	s.Handle("textDocument/hover", sc.positional(func(sym *symbol, _ json.RawMessage) (any, error) {
		value := "```go\n" + sym.signature + "\n```\n\n---\n\n"
		if sym.doc != "" {
			value += sym.doc + "\n\n\n"
		}
		if sym.methods != "" {
			value += "```go\n" + sym.methods + "\n```\n\n---\n\n"
		} else if sym.doc != "" {
			value += "---\n\n"
		}
		value += "[`" + sym.linkName + "` on pkg.go.dev](https://pkg.go.dev/example.com/sample#" + sym.key + ")"
		return map[string]any{
			"contents": map[string]any{"kind": "markdown", "value": value},
			"range":    sym.sel,
		}, nil
	}))
	s.Handle("textDocument/prepareCallHierarchy", sc.positional(func(sym *symbol, _ json.RawMessage) (any, error) {
		if sym.kind != KindFunction && sym.kind != KindMethod {
			return nil, errNotApplicable(sym.name + " is not a function")
		}
		return []callHierarchyItem{sc.item(sym)}, nil
	}))
	s.Handle("callHierarchy/incomingCalls", sc.hierarchy(func(sym *symbol) any {
		out := []map[string]any{}
		for _, c := range sc.calls {
			if c.to == sym.key || c.dynamic == sym.key {
				out = append(out, map[string]any{"from": sc.item(sc.syms[c.from]), "fromRanges": c.sites})
			}
		}
		return out
	}))
	s.Handle("callHierarchy/outgoingCalls", sc.hierarchy(func(sym *symbol) any {
		out := []map[string]any{}
		for _, c := range sc.calls {
			if c.from == sym.key {
				out = append(out, map[string]any{"to": sc.item(sc.syms[c.to]), "fromRanges": c.sites})
			}
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
		if path, err := uriPath(params.TextDocument.URI); err != nil || path != sc.path {
			return nil, nil
		}
		// Hierarchical, as gopls answers: interface methods are children
		// of their interface; everything else is top level.
		var top []map[string]any
		var shape map[string]any
		for _, key := range sc.order {
			sym := sc.syms[key]
			ds := map[string]any{
				"name": sym.outlineName(), "kind": sym.kind,
				"range": sym.full, "selectionRange": sym.sel,
			}
			switch key {
			case "Shape":
				shape = ds
				top = append(top, ds)
			case "Shape.Area":
				shape["children"] = []map[string]any{ds}
			default:
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

func sortSpans(s []span) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Start.Line != s[j].Start.Line {
			return s[i].Start.Line < s[j].Start.Line
		}
		return s[i].Start.Character < s[j].Start.Character
	})
}

type location struct {
	URI   string `json:"uri"`
	Range span   `json:"range"`
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

type symbol struct {
	key       string // unique, e.g. "Rect.Area"
	name      string
	kind      int
	decl      string // unique line prefix that declares the symbol
	typeOf    string // key of the symbol's result type, or "float64"
	full, sel span
	signature string // as gopls hovers it; derived from the source if empty
	doc       string
	linkName  string // the pkg.go.dev link text
	methods   string // a struct's method set, as gopls hovers it
	uses      []span // a type's uses, for references and definition
}

func (sym *symbol) isType() bool { return sym.kind == KindInterface || sym.kind == KindStruct }

// outlineName is the documentSymbol name: gopls writes "(T).M" for a
// concrete method, and plain "M" for an interface method.
func (sym *symbol) outlineName() string {
	if recv, _, ok := strings.Cut(sym.key, "."); ok && sym.kind == KindMethod && strings.HasPrefix(sym.decl, "func (") {
		return "(" + recv + ")." + sym.name
	}
	return sym.name
}

type call struct {
	from, to string
	needle   string
	sites    []span
	dynamic  string // a concrete method the call reaches through the interface
}

type sampleScenario struct {
	mu    sync.Mutex
	path  string // absolute path of shapes.go
	uri   string
	syms  map[string]*symbol
	order []string
	calls []*call
}

// sampleSymbols describes every declaration in shapes.go.
func sampleSymbols() []*symbol {
	return []*symbol{
		{key: "Shape", name: "Shape", kind: KindInterface, decl: "type Shape interface"},
		{key: "Shape.Area", name: "Area", kind: KindMethod, decl: "\tArea() float64", typeOf: "float64",
			signature: "func (Shape) Area() float64", linkName: "(sample.Shape).Area"},
		{key: "Rect", name: "Rect", kind: KindStruct, decl: "type Rect struct", methods: "func (r Rect) Area() float64"},
		{key: "Rect.Area", name: "Area", kind: KindMethod, decl: "func (r Rect) Area()", typeOf: "float64",
			linkName: "(sample.Rect).Area"},
		{key: "NewRect", name: "NewRect", kind: KindFunction, decl: "func NewRect(", typeOf: "Rect",
			signature: "func NewRect(w float64, h float64) Rect"},
		{key: "TotalArea", name: "TotalArea", kind: KindFunction, decl: "func TotalArea(", typeOf: "float64"},
		{key: "Report", name: "Report", kind: KindFunction, decl: "func Report(", typeOf: "float64"},
	}
}

func sampleCalls() []*call {
	return []*call{
		{from: "TotalArea", to: "Shape.Area", needle: "Area(", dynamic: "Rect.Area"},
		{from: "Report", to: "TotalArea", needle: "TotalArea("},
		{from: "Report", to: "NewRect", needle: "NewRect("},
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
	path := filepath.Join(dir, SampleFile)
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(src), "\n")

	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.path = path
	sc.uri = (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	sc.syms = map[string]*symbol{}
	sc.order = nil
	for _, sym := range sampleSymbols() {
		if err := sym.locate(lines); err != nil {
			return err
		}
		sc.syms[sym.key] = sym
		sc.order = append(sc.order, sym.key)
		if sym.isType() {
			sym.uses = typeUses(lines, sym)
		}
	}
	sc.calls = sampleCalls()
	for _, c := range sc.calls {
		from := sc.syms[c.from]
		for l := from.full.Start.Line + 1; l <= from.full.End.Line; l++ {
			line := lines[l]
			for off := 0; ; {
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

// locate fills the symbol's ranges, signature and doc from the file lines.
// The fixture is ASCII, so byte offsets equal UTF-16 offsets.
func (sym *symbol) locate(lines []string) error {
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
			sym.linkName = "sample." + sym.key
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

// typeUses finds the uses of a type: its name as a whole identifier, outside
// comments and its own declaration.
func typeUses(lines []string, sym *symbol) []span {
	isIdent := func(b byte) bool {
		return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
	}
	var out []span
	for l, line := range lines {
		if l == sym.sel.Start.Line || strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		for off := 0; ; {
			i := strings.Index(line[off:], sym.name)
			if i < 0 {
				break
			}
			col, end := off+i, off+i+len(sym.name)
			if (col == 0 || !isIdent(line[col-1])) && (end == len(line) || !isIdent(line[end])) {
				out = append(out, span{position{l, col}, position{l, end}})
			}
			off = end
		}
	}
	return out
}

func (sc *sampleScenario) location(sp span) location {
	return location{URI: sc.uri, Range: sp}
}

func (sc *sampleScenario) item(sym *symbol) callHierarchyItem {
	data, _ := json.Marshal(map[string]string{"key": sym.key})
	return callHierarchyItem{
		Name:           sym.name,
		Detail:         "example.com/sample • " + SampleFile,
		URI:            sc.uri,
		Kind:           KindFunction,
		Range:          sym.sel,
		SelectionRange: sym.sel,
		Data:           data,
	}
}

// at returns the symbol at pos: the callee when pos is on a call site, the
// declared symbol when pos is on a declaration's name, otherwise nil.
func (sc *sampleScenario) at(uri string, pos position) *symbol {
	if p, err := uriPath(uri); err != nil || p != sc.path {
		return nil
	}
	for _, c := range sc.calls {
		for _, site := range c.sites {
			if site.contains(pos) {
				return sc.syms[c.to]
			}
		}
	}
	for _, key := range sc.order {
		sym := sc.syms[key]
		if sym.sel.contains(pos) {
			return sym
		}
		for _, use := range sym.uses {
			if use.contains(pos) {
				return sym
			}
		}
	}
	return nil
}

// positional adapts f to a textDocument/* request. It answers null when no
// symbol is at the requested position.
func (sc *sampleScenario) positional(f func(*symbol, json.RawMessage) (any, error)) Handler {
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
		return f(sym, p)
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
