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
func Sample(s *Server) {
	sc := &sampleScenario{}
	s.Handle("initialise", func(_ context.Context, p json.RawMessage) (any, error) {
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
	})
	s.Handle("textDocument/definition", sc.positional(func(sym *symbol, _ json.RawMessage) any {
		return sc.location(sym.sel)
	}))
	s.Handle("textDocument/typeDefinition", sc.positional(func(sym *symbol, _ json.RawMessage) any {
		if sym.typeOf == "" {
			return nil
		}
		return sc.location(sc.syms[sym.typeOf].sel)
	}))
	s.Handle("textDocument/implementation", sc.positional(func(sym *symbol, _ json.RawMessage) any {
		out := []location{}
		for _, key := range sc.implementations(sym.key) {
			out = append(out, sc.location(sc.syms[key].sel))
		}
		return out
	}))
	s.Handle("textDocument/references", sc.positional(func(sym *symbol, p json.RawMessage) any {
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
			if c.to == sym.key {
				spans = append(spans, c.sites...)
			}
		}
		sortSpans(spans)
		out := []location{}
		for _, sp := range spans {
			out = append(out, sc.location(sp))
		}
		return out
	}))
	s.Handle("textDocument/hover", sc.positional(func(sym *symbol, _ json.RawMessage) any {
		value := "```go\n" + sym.signature + "\n```"
		if sym.doc != "" {
			value += "\n\n" + sym.doc
		}
		return map[string]any{
			"contents": map[string]any{"kind": "markdown", "value": value},
			"range":    sym.sel,
		}
	}))
	s.Handle("textDocument/prepareCallHierarchy", sc.positional(func(sym *symbol, _ json.RawMessage) any {
		if sym.kind != KindFunction && sym.kind != KindMethod {
			return nil
		}
		return []callHierarchyItem{sc.item(sym)}
	}))
	s.Handle("callHierarchy/incomingCalls", sc.hierarchy(func(sym *symbol) any {
		out := []map[string]any{}
		for _, c := range sc.calls {
			if c.to == sym.key {
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
				"name": sym.name, "kind": sym.kind,
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
	for _, n := range []string{"textDocument/didOpen", "textDocument/didClose", "initialised"} {
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
	typeOf    string // key of the symbol's type, for typeDefinition
	full, sel span
	signature string
	doc       string
}

type call struct {
	from, to string
	needle   string
	sites    []span
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
		{key: "Shape.Area", name: "Area", kind: KindMethod, decl: "\tArea() float64"},
		{key: "Rect", name: "Rect", kind: KindStruct, decl: "type Rect struct"},
		{key: "Rect.Area", name: "Area", kind: KindMethod, decl: "func (r Rect) Area()"},
		{key: "NewRect", name: "NewRect", kind: KindFunction, decl: "func NewRect(", typeOf: "Rect"},
		{key: "TotalArea", name: "TotalArea", kind: KindFunction, decl: "func TotalArea("},
		{key: "Report", name: "Report", kind: KindFunction, decl: "func Report("},
	}
}

func sampleCalls() []*call {
	return []*call{
		{from: "TotalArea", to: "Shape.Area", needle: "Area("},
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
		sym.full = span{position{i, 0}, position{end, len(lines[end])}}
		sym.sel = span{position{i, nameCol}, position{i, nameCol + len(sym.name)}}
		sig, _, _ := strings.Cut(strings.TrimSpace(line), " {")
		sym.signature = sig
		if i > 0 {
			if doc, ok := strings.CutPrefix(strings.TrimSpace(lines[i-1]), "// "); ok {
				sym.doc = doc
			}
		}
		return nil
	}
	return fmt.Errorf("lspfake: declaration %q not found", sym.decl)
}

func (sc *sampleScenario) location(sp span) location {
	return location{URI: sc.uri, Range: sp}
}

func (sc *sampleScenario) item(sym *symbol) callHierarchyItem {
	data, _ := json.Marshal(map[string]string{"key": sym.key})
	return callHierarchyItem{
		Name:           sym.name,
		Kind:           sym.kind,
		Detail:         "example.com/sample • " + SampleFile,
		URI:            sc.uri,
		Range:          sym.full,
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
		if sc.syms[key].sel.contains(pos) {
			return sc.syms[key]
		}
	}
	return nil
}

// positional adapts f to a textDocument/* request. It answers null when no
// symbol is at the requested position.
func (sc *sampleScenario) positional(f func(*symbol, json.RawMessage) any) Handler {
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
		return f(sym, p), nil
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
