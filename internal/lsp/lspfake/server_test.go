package lspfake_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
)

// peer is a bare-bones JSON-RPC client used to drive the fake in tests.
type peer struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	done chan error
}

func start(t *testing.T, s *lspfake.Server) *peer {
	t.Helper()
	client, server := net.Pipe()
	p := &peer{t: t, conn: client, r: bufio.NewReader(client), done: make(chan error, 1)}
	go func() { p.done <- s.Serve(context.Background(), server); server.Close() }()
	t.Cleanup(func() { client.Close() })
	return p
}

func (p *peer) send(msg map[string]any) {
	p.t.Helper()
	msg["jsonrpc"] = "2.0"
	body, err := json.Marshal(msg)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := lspfake.WriteFrame(p.conn, body); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) recv() lspfake.Message {
	p.t.Helper()
	_ = p.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	body, err := lspfake.ReadFrame(p.r)
	if err != nil {
		p.t.Fatalf("recv: %v", err)
	}
	var m lspfake.Message
	if err := json.Unmarshal(body, &m); err != nil {
		p.t.Fatal(err)
	}
	return m
}

func (p *peer) call(id int, method string, params any) lspfake.Message {
	p.t.Helper()
	p.send(map[string]any{"id": id, "method": method, "params": params})
	return p.recv()
}

func (p *peer) exit() error {
	p.t.Helper()
	p.send(map[string]any{"method": "exit"})
	select {
	case err := <-p.done:
		return err
	case <-time.After(5 * time.Second):
		p.t.Fatal("Serve did not return after exit")
		return nil
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	bodies := []string{`{"a":1}`, `{"text":"héllo 😀"}`, `{}`}
	for _, b := range bodies {
		if err := lspfake.WriteFrame(&buf, []byte(b)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(buf.String(), "Content-Length: 7\r\n\r\n{\"a\":1}") {
		t.Fatalf("unexpected framing: %q", buf.String())
	}
	r := bufio.NewReader(&buf)
	for _, want := range bodies {
		got, err := lspfake.ReadFrame(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("ReadFrame = %q, want %q", got, want)
		}
	}
	if _, err := lspfake.ReadFrame(r); err != io.EOF {
		t.Errorf("ReadFrame at end = %v, want io.EOF", err)
	}
}

func TestReadFrameHeaders(t *testing.T) {
	in := "content-length: 2\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n{}"
	got, err := lspfake.ReadFrame(bufio.NewReader(strings.NewReader(in)))
	if err != nil || string(got) != "{}" {
		t.Fatalf("ReadFrame = %q, %v", got, err)
	}
	for _, bad := range []string{
		"Content-Type: x\r\n\r\n{}",     // no length
		"Content-Length: abc\r\n\r\n{}", // not a number
		"garbage\r\n\r\n",               // no colon
		"Content-Length: 10\r\n\r\n{}",  // short body
		"Content-Length: 2\r\n",         // truncated headers
	} {
		if _, err := lspfake.ReadFrame(bufio.NewReader(strings.NewReader(bad))); err == nil {
			t.Errorf("ReadFrame(%q) succeeded, want error", bad)
		}
	}
}

func TestDefaultsAndRecording(t *testing.T) {
	s := lspfake.New()
	p := start(t, s)

	init := p.call(1, "initialise", map[string]any{"processId": 1})
	if init.Error != nil || !strings.Contains(string(init.Result), "capabilities") {
		t.Fatalf("initialise = %+v", init)
	}
	p.send(map[string]any{"method": "initialised", "params": map[string]any{}})
	if m := p.call(2, "no/such", nil); m.Error == nil || m.Error.Code != lspfake.CodeMethodNotFound {
		t.Errorf("unknown method error = %+v, want code %d", m.Error, lspfake.CodeMethodNotFound)
	}
	if m := p.call(3, "shutdown", nil); m.Error != nil || string(m.Result) != "null" {
		t.Errorf("shutdown = %+v, want null result", m)
	}
	if err := p.exit(); err != nil {
		t.Errorf("Serve after exit = %v, want nil", err)
	}

	want := []string{"initialise", "initialised", "no/such", "shutdown", "exit"}
	if got := s.Methods(); !reflect.DeepEqual(got, want) {
		t.Errorf("Methods = %v, want %v", got, want)
	}
	reqs := s.Requests("initialise")
	if len(reqs) != 1 || !reqs[0].IsRequest() || string(reqs[0].Params) != `{"processId":1}` {
		t.Errorf("Requests(initialise) = %+v", reqs)
	}
	if n := s.Requests("initialised"); len(n) != 1 || !n[0].IsNotification() {
		t.Errorf("Requests(initialised) = %+v", n)
	}
}

func TestServeReturnsEOFWithoutExit(t *testing.T) {
	s := lspfake.New()
	p := start(t, s)
	p.conn.Close()
	select {
	case err := <-p.done:
		if !errors.Is(err, io.EOF) {
			t.Errorf("Serve = %v, want io.EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return on EOF")
	}
}

func TestHandlerErrors(t *testing.T) {
	s := lspfake.New()
	s.Handle("rpc/err", func(context.Context, json.RawMessage) (any, error) {
		return nil, &lspfake.Error{Code: 42, Message: "custom"}
	})
	s.Handle("plain/err", func(context.Context, json.RawMessage) (any, error) {
		return nil, errors.New("boom")
	})
	p := start(t, s)
	if m := p.call(1, "rpc/err", nil); m.Error == nil || m.Error.Code != 42 || m.Error.Message != "custom" {
		t.Errorf("rpc/err = %+v", m.Error)
	}
	if m := p.call(2, "plain/err", nil); m.Error == nil || m.Error.Code != lspfake.CodeInternalError {
		t.Errorf("plain/err = %+v", m.Error)
	}
}

func TestConcurrentRequestsAnswerOutOfOrder(t *testing.T) {
	s := lspfake.New()
	release := make(chan struct{})
	s.Handle("slow", func(ctx context.Context, _ json.RawMessage) (any, error) {
		select {
		case <-release:
			return "slow", nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	s.Handle("fast", func(context.Context, json.RawMessage) (any, error) {
		return "fast", nil
	})
	p := start(t, s)
	p.send(map[string]any{"id": 1, "method": "slow"})
	p.send(map[string]any{"id": 2, "method": "fast"})
	first := p.recv() // slow is still blocked, so this must be fast
	close(release)
	second := p.recv()
	if string(first.ID) != "2" || string(second.ID) != "1" {
		t.Errorf("response order = %s, %s; want 2, 1", first.ID, second.ID)
	}
}

func TestCancelRequest(t *testing.T) {
	s := lspfake.New()
	s.Handle("block", func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	p := start(t, s)
	p.send(map[string]any{"id": 7, "method": "block"})
	p.send(map[string]any{"method": "$/cancelRequest", "params": map[string]any{"id": 7}})
	m := p.recv()
	if string(m.ID) != "7" || m.Error == nil || m.Error.Code != lspfake.CodeRequestCancelled {
		t.Errorf("cancelled response = %+v, want id 7 with code %d", m, lspfake.CodeRequestCancelled)
	}
}

func TestServerInitiatedCall(t *testing.T) {
	s := lspfake.New()
	s.Handle("initialise", func(ctx context.Context, _ json.RawMessage) (any, error) {
		if err := s.Notify("window/logMessage", map[string]any{"type": 3, "message": "hi"}); err != nil {
			return nil, err
		}
		res, err := s.Call(ctx, "window/workDoneProgress/create", map[string]any{"token": "t"})
		if err != nil {
			return nil, err
		}
		return map[string]any{"capabilities": map[string]any{}, "echo": res}, nil
	})
	p := start(t, s)
	p.send(map[string]any{"id": 1, "method": "initialise"})

	if n := p.recv(); n.Method != "window/logMessage" || !n.IsNotification() {
		t.Fatalf("first message = %+v, want logMessage notification", n)
	}
	req := p.recv()
	if req.Method != "window/workDoneProgress/create" || !req.IsRequest() {
		t.Fatalf("second message = %+v, want server request", req)
	}
	p.send(map[string]any{"id": req.ID, "result": "ok"})
	resp := p.recv()
	if string(resp.ID) != "1" || !strings.Contains(string(resp.Result), `"echo":"ok"`) {
		t.Errorf("initialise response = %+v", resp)
	}
}

// sampleSession starts the Sample scenario and initialises it with the real
// testdata/sample root.
func sampleSession(t *testing.T) (*peer, string) {
	t.Helper()
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	s := lspfake.New()
	lspfake.Sample(s)
	p := start(t, s)
	rootURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(root)}).String()
	if m := p.call(0, "initialise", map[string]any{"rootUri": rootURI}); m.Error != nil {
		t.Fatalf("initialise: %v", m.Error)
	}
	return p, rootURI + "/" + lspfake.SampleFile
}

func at(uri string, line, char int) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": char},
	}
}

type item struct {
	Name           string          `json:"name"`
	Kind           int             `json:"kind"`
	URI            string          `json:"uri"`
	Range          json.RawMessage `json:"range"`
	SelectionRange json.RawMessage `json:"selectionRange"`
	Data           json.RawMessage `json:"data"`
}

func TestSampleCallHierarchy(t *testing.T) {
	p, uri := sampleSession(t)

	// Line 18 col 6 (1-based) is the name in "func TotalArea(".
	m := p.call(1, "textDocument/prepareCallHierarchy", at(uri, 17, 5))
	var items []item
	if err := json.Unmarshal(m.Result, &items); err != nil || len(items) != 1 {
		t.Fatalf("prepare = %s, %v", m.Result, m.Error)
	}
	ego := items[0]
	if ego.Name != "TotalArea" || ego.Kind != lspfake.KindFunction || ego.URI != uri {
		t.Errorf("ego = %+v", ego)
	}
	if got := string(ego.SelectionRange); got != `{"start":{"line":17,"character":5},"end":{"line":17,"character":14}}` {
		t.Errorf("selectionRange = %s", got)
	}
	// gopls answers with the name as the item's range, and kind Function
	// for methods too; the client must take both from the outline.
	if got := string(ego.Range); got != string(ego.SelectionRange) {
		t.Errorf("range = %s, want the selection range", got)
	}
	m = p.call(7, "textDocument/prepareCallHierarchy", at(uri, 11, 15))
	var rectArea []item // not items: decoding into it would overwrite ego's raw fields
	if err := json.Unmarshal(m.Result, &rectArea); err != nil || len(rectArea) != 1 || rectArea[0].Kind != lspfake.KindFunction {
		t.Fatalf("prepare on Rect.Area = %s, want one item of kind Function", m.Result)
	}
	// Dynamic dispatch: TotalArea calls Rect.Area through Shape, so gopls
	// lists it as a caller of Rect.Area but not as a callee of TotalArea.
	in := p.call(8, "callHierarchy/incomingCalls", map[string]any{"item": rectArea[0]})
	if !strings.Contains(string(in.Result), `"name":"TotalArea"`) {
		t.Errorf("incomingCalls of Rect.Area = %s, want TotalArea", in.Result)
	}

	in = p.call(2, "callHierarchy/incomingCalls", map[string]any{"item": ego})
	want := `[{"from":` // Report calls TotalArea once, at line 28 col 9 (1-based).
	if !strings.HasPrefix(string(in.Result), want) ||
		!strings.Contains(string(in.Result), `"name":"Report"`) ||
		!strings.Contains(string(in.Result), `"fromRanges":[{"start":{"line":27,"character":8},"end":{"line":27,"character":17}}]`) {
		t.Errorf("incomingCalls = %s", in.Result)
	}

	out := p.call(3, "callHierarchy/outgoingCalls", map[string]any{"item": ego})
	if !strings.Contains(string(out.Result), `"name":"Area"`) || !strings.Contains(string(out.Result), `"line":20`) {
		t.Errorf("outgoingCalls = %s", out.Result)
	}
	if strings.Count(string(out.Result), `"to":`) != 1 {
		t.Errorf("outgoingCalls = %s, want only Shape.Area", out.Result)
	}

	// Report calls NewRect twice: weight 2.
	m = p.call(4, "textDocument/prepareCallHierarchy", at(uri, 26, 5))
	_ = json.Unmarshal(m.Result, &items)
	out = p.call(5, "callHierarchy/outgoingCalls", map[string]any{"item": items[0]})
	var calls []struct {
		To         item              `json:"to"`
		FromRanges []json.RawMessage `json:"fromRanges"`
	}
	if err := json.Unmarshal(out.Result, &calls); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range calls {
		got[c.To.Name] = len(c.FromRanges)
	}
	if !reflect.DeepEqual(got, map[string]int{"TotalArea": 1, "NewRect": 2}) {
		t.Errorf("Report outgoing = %v", got)
	}

	// The item data must be sent back, as with gopls.
	ego.Data = nil
	if m := p.call(6, "callHierarchy/incomingCalls", map[string]any{"item": ego}); m.Error == nil || m.Error.Code != lspfake.CodeInvalidParams {
		t.Errorf("incomingCalls without data = %+v, want InvalidParams", m.Error)
	}
}

func TestSampleTextDocumentRequests(t *testing.T) {
	p, uri := sampleSession(t)
	loc := func(line, from, to int) string {
		return `{"uri":"` + uri + `","range":{"start":{"line":` + itoa(line) + `,"character":` + itoa(from) +
			`},"end":{"line":` + itoa(line) + `,"character":` + itoa(to) + `}}}`
	}
	builtin := `{"uri":"` + lspfake.SampleBuiltinURI + `","range":{"start":{"line":59,"character":5},"end":{"line":59,"character":12}}}`
	tests := []struct {
		name   string
		method string
		params map[string]any
		want   string // the result, or "error" for an error response
	}{
		{"definition from call site", "textDocument/definition", at(uri, 27, 10), loc(17, 5, 14)},
		{"definition from a type use", "textDocument/definition", at(uri, 17, 26), loc(3, 5, 10)},
		{"definition on blank", "textDocument/definition", at(uri, 1, 0), "null"},
		{"typeDefinition of NewRect", "textDocument/typeDefinition", at(uri, 14, 6), "[" + loc(8, 5, 9) + "]"},
		{"typeDefinition of a type", "textDocument/typeDefinition", at(uri, 3, 6), "[" + loc(3, 5, 10) + "]"},
		{"typeDefinition of TotalArea", "textDocument/typeDefinition", at(uri, 17, 6), "[" + builtin + "]"},
		{"typeDefinition of Shape.Area", "textDocument/typeDefinition", at(uri, 4, 2), "[" + builtin + "]"},
		{"implementation of Shape", "textDocument/implementation", at(uri, 3, 6), "[" + loc(8, 5, 9) + "]"},
		{"implementation of Rect.Area", "textDocument/implementation", at(uri, 11, 15), "[" + loc(4, 1, 5) + "]"},
		{"implementation of a function", "textDocument/implementation", at(uri, 17, 6), "error"},
		{"references without decl", "textDocument/references",
			withContext(at(uri, 17, 5), false), "[" + loc(27, 8, 17) + "]"},
		{"references with decl", "textDocument/references",
			withContext(at(uri, 17, 5), true), "[" + loc(17, 5, 14) + "," + loc(27, 8, 17) + "]"},
		{"references through dynamic dispatch", "textDocument/references",
			withContext(at(uri, 11, 15), false), "[" + loc(20, 13, 17) + "]"},
		{"references of a type", "textDocument/references",
			withContext(at(uri, 8, 5), false), "[" + loc(11, 8, 12) + "," + loc(14, 27, 31) + "," + loc(14, 41, 45) + "]"},
		{"prepare on a type", "textDocument/prepareCallHierarchy", at(uri, 8, 5), "error"},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := p.call(10+i, tc.method, tc.params)
			if tc.want == "error" {
				if m.Error == nil {
					t.Errorf("%s = %s, want an error response", tc.method, m.Result)
				}
				return
			}
			if m.Error != nil || string(m.Result) != tc.want {
				t.Errorf("%s = %s (err %v)\nwant %s", tc.method, m.Result, m.Error, tc.want)
			}
		})
	}

	ds := p.call(98, "textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": uri}})
	var syms []struct {
		Name     string          `json:"name"`
		Kind     int             `json:"kind"`
		Range    json.RawMessage `json:"range"`
		Children []struct {
			Name  string          `json:"name"`
			Range json.RawMessage `json:"range"`
		} `json:"children"`
	}
	if err := json.Unmarshal(ds.Result, &syms); err != nil {
		t.Fatalf("documentSymbol = %s: %v", ds.Result, err)
	}
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
	}
	if want := []string{"Shape", "Rect", "(Rect).Area", "NewRect", "TotalArea", "Report"}; !reflect.DeepEqual(names, want) {
		t.Errorf("documentSymbol names = %v, want %v", names, want)
	}
	if syms[0].Kind != lspfake.KindInterface || len(syms[0].Children) != 1 || syms[0].Children[0].Name != "Area" {
		t.Errorf("Shape symbol = %+v, want interface with child Area", syms[0])
	}
	// As with gopls, a type's range starts at its name, not at "type", and
	// so does an interface method's.
	if got, want := string(syms[0].Range), `{"start":{"line":3,"character":5},"end":{"line":5,"character":1}}`; got != want {
		t.Errorf("Shape range = %s, want %s", got, want)
	}
	if got, want := string(syms[0].Children[0].Range), `{"start":{"line":4,"character":1},"end":{"line":4,"character":15}}`; got != want {
		t.Errorf("Shape.Area range = %s, want %s", got, want)
	}

	hovers := []struct {
		line, char int
		want       string
	}{
		{17, 5, "```go\nfunc TotalArea(shapes []Shape) float64\n```\n\n---\n\nTotalArea sums the area of every shape.\n\n\n---\n\n" +
			"[`sample.TotalArea` on pkg.go.dev](https://pkg.go.dev/example.com/sample#TotalArea)"},
		{4, 1, "```go\nfunc (Shape) Area() float64\n```\n\n---\n\n" +
			"[`(sample.Shape).Area` on pkg.go.dev](https://pkg.go.dev/example.com/sample#Shape.Area)"},
		{3, 5, "```go\ntype Shape interface { // size=16 (0x10)\n\tArea() float64\n}\n```\n\n---\n\nShape is anything with an area.\n\n\n---\n\n" +
			"[`sample.Shape` on pkg.go.dev](https://pkg.go.dev/example.com/sample#Shape)"},
		{8, 5, "```go\ntype Rect struct{ W, H float64 } // size=16 (0x10)\n```\n\n---\n\nRect is an axis-aligned rectangle.\n\n\n" +
			"```go\nfunc (r Rect) Area() float64\n```\n\n---\n\n[`sample.Rect` on pkg.go.dev](https://pkg.go.dev/example.com/sample#Rect)"},
		{14, 5, "```go\nfunc NewRect(w float64, h float64) Rect\n```\n\n---\n\nNewRect builds a Rect.\n\n\n---\n\n" +
			"[`sample.NewRect` on pkg.go.dev](https://pkg.go.dev/example.com/sample#NewRect)"},
	}
	for i, h := range hovers {
		m := p.call(100+i, "textDocument/hover", at(uri, h.line, h.char))
		var result struct {
			Contents struct {
				Value string `json:"value"`
			} `json:"contents"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil || result.Contents.Value != h.want {
			t.Errorf("hover at %d:%d = %q\nwant %q", h.line, h.char, result.Contents.Value, h.want)
		}
	}
}

func TestSampleRequiresInitialise(t *testing.T) {
	s := lspfake.New()
	lspfake.Sample(s)
	p := start(t, s)
	m := p.call(1, "textDocument/definition", at("file:///x/shapes.go", 0, 0))
	if m.Error == nil {
		t.Errorf("definition before initialise succeeded: %s", m.Result)
	}
}

func withContext(params map[string]any, includeDecl bool) map[string]any {
	params["context"] = map[string]any{"includeDeclaration": includeDecl}
	return params
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestSampleAcceptsBothHandshakeSpellings(t *testing.T) {
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	rootURI := (&url.URL{Scheme: "file", Path: filepath.ToSlash(root)}).String()
	for _, method := range []string{"initialise", "initialize"} {
		s := lspfake.New()
		lspfake.Sample(s)
		p := start(t, s)
		if m := p.call(1, method, map[string]any{"rootUri": rootURI}); m.Error != nil || !strings.Contains(string(m.Result), "callHierarchyProvider") {
			t.Errorf("%s = %+v", method, m)
		}
	}
}
