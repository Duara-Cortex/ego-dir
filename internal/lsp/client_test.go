// Acceptance tests for the JSON-RPC / LSP client.
package lsp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
)

func TestMain(m *testing.M) {
	lspfake.RunHelperIfRequested()
	os.Exit(m.Run())
}

type session struct {
	client *lsp.Client
	server *lspfake.Server
	conn   net.Conn // server end of the pipe
	done   chan error
	root   string
	uri    string // shapes.go
}

// connect runs s on one end of a pipe and an lsp.Client on the other.
func connect(t *testing.T, s *lspfake.Server) *session {
	t.Helper()
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	clientEnd, serverEnd := net.Pipe()
	sess := &session{server: s, conn: serverEnd, done: make(chan error, 1), root: root,
		uri: lsp.FileURI(filepath.Join(root, lspfake.SampleFile))}
	go func() { sess.done <- s.Serve(context.Background(), serverEnd); serverEnd.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := lsp.NewClient(ctx, clientEnd, root)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	sess.client = c
	t.Cleanup(func() { clientEnd.Close(); serverEnd.Close() })
	return sess
}

func sample(t *testing.T) *session {
	t.Helper()
	s := lspfake.New()
	lspfake.Sample(s)
	return connect(t, s)
}

func ctx5(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func pos(line, char int) lsp.Position { return lsp.Position{Line: line, Character: char} }

func TestLifecycle(t *testing.T) {
	sess := sample(t)
	if err := sess.client.Close(ctx5(t)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-sess.done:
		if err != nil {
			t.Errorf("server Serve = %v; the client must send exit after shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive exit")
	}

	want := []string{"initialise", "initialised", "shutdown", "exit"}
	if got := sess.server.Methods(); !reflect.DeepEqual(got, want) {
		t.Errorf("methods = %v, want %v", got, want)
	}
	init := sess.server.Requests("initialise")[0]
	if !init.IsRequest() {
		t.Error("initialise must be a request")
	}
	var params struct {
		ProcessID    *int            `json:"processId"`
		RootURI      string          `json:"rootUri"`
		Capabilities json.RawMessage `json:"capabilities"`
		Folders      []struct {
			URI string `json:"uri"`
		} `json:"workspaceFolders"`
	}
	if err := json.Unmarshal(init.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ProcessID == nil || *params.ProcessID != os.Getpid() {
		t.Errorf("processId = %v, want %d", params.ProcessID, os.Getpid())
	}
	if params.RootURI != lsp.FileURI(sess.root) {
		t.Errorf("rootUri = %q, want %q", params.RootURI, lsp.FileURI(sess.root))
	}
	if len(params.Folders) != 1 || params.Folders[0].URI != lsp.FileURI(sess.root) {
		t.Errorf("workspaceFolders = %+v", params.Folders)
	}
	for _, capability := range []string{"callHierarchy", "definition", "references", "typeDefinition", "implementation", "hover", "documentSymbol"} {
		if !strings.Contains(string(params.Capabilities), capability) {
			t.Errorf("client capabilities do not mention %q: %s", capability, params.Capabilities)
		}
	}
	if !sess.server.Requests("initialised")[0].IsNotification() {
		t.Error("initialised must be a notification")
	}
	if !sess.server.Requests("exit")[0].IsNotification() {
		t.Error("exit must be a notification")
	}

	if _, err := sess.client.Definition(ctx5(t), sess.uri, pos(27, 10)); err == nil {
		t.Error("request after Close succeeded, want error")
	}
}

func TestInitialiseError(t *testing.T) {
	s := lspfake.New()
	s.Handle("initialise", func(context.Context, json.RawMessage) (any, error) {
		return nil, &lspfake.Error{Code: -32603, Message: "cannot start"}
	})
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close()
	defer serverEnd.Close()
	go s.Serve(context.Background(), serverEnd)
	_, err := lsp.NewClient(ctx5(t), clientEnd, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Errorf("NewClient error = %v, want it to carry the server message", err)
	}
}

func TestDidOpen(t *testing.T) {
	sess := sample(t)
	if err := sess.client.DidOpen(ctx5(t), sess.uri, "go", "package sample\n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, sess.server, "textDocument/didOpen")
	m := sess.server.Requests("textDocument/didOpen")[0]
	var p struct {
		TextDocument struct {
			URI        string `json:"uri"`
			LanguageID string `json:"languageId"`
			Version    int    `json:"version"`
			Text       string `json:"text"`
		} `json:"textDocument"`
	}
	_ = json.Unmarshal(m.Params, &p)
	if !m.IsNotification() || p.TextDocument.URI != sess.uri || p.TextDocument.LanguageID != "go" || p.TextDocument.Text != "package sample\n" {
		t.Errorf("didOpen = %s", m.Params)
	}
}

func TestTextDocumentRequests(t *testing.T) {
	sess := sample(t)
	ctx := ctx5(t)
	loc := func(line, from, to int) lsp.Location {
		return lsp.Location{URI: sess.uri, Range: lsp.Range{Start: pos(line, from), End: pos(line, to)}}
	}

	got, err := sess.client.Definition(ctx, sess.uri, pos(27, 10))
	if err != nil || !reflect.DeepEqual(got, []lsp.Location{loc(17, 5, 14)}) {
		t.Errorf("Definition = %+v, %v", got, err)
	}
	got, err = sess.client.Definition(ctx, sess.uri, pos(1, 0))
	if err != nil || len(got) != 0 {
		t.Errorf("Definition(null) = %+v, %v; want empty, no error", got, err)
	}
	got, err = sess.client.TypeDefinition(ctx, sess.uri, pos(14, 6))
	if err != nil || !reflect.DeepEqual(got, []lsp.Location{loc(8, 5, 9)}) {
		t.Errorf("TypeDefinition = %+v, %v", got, err)
	}
	got, err = sess.client.Implementation(ctx, sess.uri, pos(3, 6))
	if err != nil || !reflect.DeepEqual(got, []lsp.Location{loc(8, 5, 9)}) {
		t.Errorf("Implementation = %+v, %v", got, err)
	}
	got, err = sess.client.References(ctx, sess.uri, pos(17, 5), false)
	if err != nil || !reflect.DeepEqual(got, []lsp.Location{loc(27, 8, 17)}) {
		t.Errorf("References(no decl) = %+v, %v", got, err)
	}
	got, err = sess.client.References(ctx, sess.uri, pos(17, 5), true)
	if err != nil || !reflect.DeepEqual(got, []lsp.Location{loc(17, 5, 14), loc(27, 8, 17)}) {
		t.Errorf("References(decl) = %+v, %v", got, err)
	}

	hover, err := sess.client.Hover(ctx, sess.uri, pos(17, 5))
	want := "```go\nfunc TotalArea(shapes []Shape) float64\n```\n\nTotalArea sums the area of every shape."
	if err != nil || hover != want {
		t.Errorf("Hover = %q, %v\nwant %q", hover, err, want)
	}

	syms, err := sess.client.DocumentSymbols(ctx, sess.uri)
	if err != nil || len(syms) != 6 || syms[0].Name != "Shape" || len(syms[0].Children) != 1 {
		t.Errorf("DocumentSymbols = %+v, %v", syms, err)
	} else if syms[0].Kind != lsp.SymbolKind(lspfake.KindInterface) || syms[0].Children[0].SelectionRange != (lsp.Range{Start: pos(4, 1), End: pos(4, 5)}) {
		t.Errorf("DocumentSymbols[0] = %+v", syms[0])
	}
}

// Servers may answer definition-like requests with Location, Location[],
// LocationLink[] or null. All must decode to []Location.
func TestLocationResultShapes(t *testing.T) {
	r := map[string]any{"start": map[string]int{"line": 3, "character": 4}, "end": map[string]int{"line": 3, "character": 9}}
	whole := map[string]any{"start": map[string]int{"line": 2, "character": 0}, "end": map[string]int{"line": 6, "character": 1}}
	want := []lsp.Location{{URI: "file:///x/a.go", Range: lsp.Range{Start: pos(3, 4), End: pos(3, 9)}}}
	shapes := map[string]any{
		"single Location": map[string]any{"uri": "file:///x/a.go", "range": r},
		"Location array":  []any{map[string]any{"uri": "file:///x/a.go", "range": r}},
		"LocationLink array": []any{map[string]any{
			"targetUri": "file:///x/a.go", "targetRange": whole, "targetSelectionRange": r,
		}},
	}
	for name, result := range shapes {
		t.Run(name, func(t *testing.T) {
			s := lspfake.New()
			s.Result("textDocument/definition", result)
			sess := connect(t, s)
			got, err := sess.client.Definition(ctx5(t), "file:///x/a.go", pos(3, 5))
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("Definition = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestHoverContentShapes(t *testing.T) {
	shapes := map[string]any{
		"MarkupContent":       map[string]any{"contents": map[string]any{"kind": "plaintext", "value": "text"}},
		"MarkedString string": map[string]any{"contents": "text"},
		"MarkedString object": map[string]any{"contents": map[string]any{"language": "go", "value": "text"}},
		"MarkedString array":  map[string]any{"contents": []any{"te", map[string]any{"language": "go", "value": "xt"}}},
	}
	want := map[string]string{
		"MarkupContent":       "text",
		"MarkedString string": "text",
		"MarkedString object": "```go\ntext\n```",
		"MarkedString array":  "te\n\n```go\nxt\n```",
	}
	for name, result := range shapes {
		t.Run(name, func(t *testing.T) {
			s := lspfake.New()
			s.Result("textDocument/hover", result)
			sess := connect(t, s)
			got, err := sess.client.Hover(ctx5(t), "file:///x/a.go", pos(0, 0))
			if err != nil || got != want[name] {
				t.Errorf("Hover = %q, %v; want %q", got, err, want[name])
			}
		})
	}
	s := lspfake.New()
	s.Result("textDocument/hover", nil)
	sess := connect(t, s)
	if got, err := sess.client.Hover(ctx5(t), "file:///x/a.go", pos(0, 0)); err != nil || got != "" {
		t.Errorf("Hover(null) = %q, %v; want empty, no error", got, err)
	}
}

func TestCallHierarchy(t *testing.T) {
	sess := sample(t)
	ctx := ctx5(t)
	items, err := sess.client.PrepareCallHierarchy(ctx, sess.uri, pos(17, 5))
	if err != nil || len(items) != 1 || items[0].Name != "TotalArea" {
		t.Fatalf("PrepareCallHierarchy = %+v, %v", items, err)
	}
	// The fake rejects items whose data field was not sent back.
	in, err := sess.client.IncomingCalls(ctx, items[0])
	if err != nil {
		t.Fatalf("IncomingCalls: %v", err)
	}
	if len(in) != 1 || in[0].From.Name != "Report" || len(in[0].FromRanges) != 1 {
		t.Errorf("IncomingCalls = %+v", in)
	}
	out, err := sess.client.OutgoingCalls(ctx, items[0])
	if err != nil || len(out) != 1 || out[0].To.Name != "Area" {
		t.Errorf("OutgoingCalls = %+v, %v", out, err)
	}

	none, err := sess.client.PrepareCallHierarchy(ctx, sess.uri, pos(8, 5)) // a type, not callable
	if err != nil || len(none) != 0 {
		t.Errorf("PrepareCallHierarchy(type) = %+v, %v; want empty", none, err)
	}
}

func TestRPCError(t *testing.T) {
	s := lspfake.New()
	s.Handle("textDocument/definition", func(context.Context, json.RawMessage) (any, error) {
		return nil, &lspfake.Error{Code: -32001, Message: "no package for file"}
	})
	sess := connect(t, s)
	_, err := sess.client.Definition(ctx5(t), "file:///x/a.go", pos(0, 0))
	var rpcErr *lsp.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error = %v (%T), want *lsp.RPCError", err, err)
	}
	if rpcErr.Code != -32001 || rpcErr.Message != "no package for file" {
		t.Errorf("RPCError = %+v", rpcErr)
	}
}

func TestServerInitiatedMessages(t *testing.T) {
	s := lspfake.New()
	lspfake.Sample(s)
	replies := make(chan error, 2)
	s.Handle("textDocument/definition", func(ctx context.Context, _ json.RawMessage) (any, error) {
		if err := s.Notify("window/logMessage", map[string]any{"type": 3, "message": "indexing"}); err != nil {
			return nil, err
		}
		// gopls sends this before reporting progress; the client must reply.
		res, err := s.Call(ctx, "window/workDoneProgress/create", map[string]any{"token": "1"})
		if err == nil && string(res) != "null" {
			err = errors.New("workDoneProgress/create result = " + string(res) + ", want null")
		}
		replies <- err
		// Unknown server requests must get some reply rather than hang.
		_, err = s.Call(ctx, "client/somethingNew", nil)
		var rpcErr *lspfake.Error
		if errors.As(err, &rpcErr) {
			err = nil
		}
		replies <- err
		return nil, nil
	})
	sess := connect(t, s)
	if _, err := sess.client.Definition(ctx5(t), sess.uri, pos(0, 0)); err != nil {
		t.Fatalf("Definition: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := <-replies; err != nil {
			t.Error(err)
		}
	}
}

func TestConcurrentRequestsOutOfOrder(t *testing.T) {
	s := lspfake.New()
	release := make(chan struct{})
	r := map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 1}}
	s.Handle("textDocument/definition", func(ctx context.Context, _ json.RawMessage) (any, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []any{map[string]any{"uri": "file:///slow.go", "range": r}}, nil
	})
	s.Handle("textDocument/typeDefinition", func(context.Context, json.RawMessage) (any, error) {
		return []any{map[string]any{"uri": "file:///fast.go", "range": r}}, nil
	})
	sess := connect(t, s)
	ctx := ctx5(t)

	slow := make(chan []lsp.Location, 1)
	go func() {
		locs, _ := sess.client.Definition(ctx, "file:///a.go", pos(0, 0))
		slow <- locs
	}()
	waitFor(t, s, "textDocument/definition")
	fast, err := sess.client.TypeDefinition(ctx, "file:///a.go", pos(0, 0))
	if err != nil || len(fast) != 1 || fast[0].URI != "file:///fast.go" {
		t.Fatalf("TypeDefinition while another request is pending = %+v, %v", fast, err)
	}
	close(release)
	if got := <-slow; len(got) != 1 || got[0].URI != "file:///slow.go" {
		t.Errorf("Definition = %+v, want the slow result routed to its own caller", got)
	}
}

func TestCancellation(t *testing.T) {
	s := lspfake.New()
	s.Handle("textDocument/definition", func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	sess := connect(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := sess.client.Definition(ctx, "file:///a.go", pos(0, 0))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("cancelled call took %v", d)
	}
	cancelMsg := waitFor(t, s, "$/cancelRequest")
	def := s.Requests("textDocument/definition")[0]
	var p struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(cancelMsg.Params, &p)
	if string(p.ID) != string(def.ID) {
		t.Errorf("$/cancelRequest id = %s, want %s", p.ID, def.ID)
	}
	// The connection must still work afterwards.
	s.Result("textDocument/typeDefinition", nil)
	if _, err := sess.client.TypeDefinition(ctx5(t), "file:///a.go", pos(0, 0)); err != nil {
		t.Errorf("request after cancellation: %v", err)
	}
}

func TestServerDiesMidRequest(t *testing.T) {
	s := lspfake.New()
	var sess *session
	s.Handle("textDocument/definition", func(context.Context, json.RawMessage) (any, error) {
		sess.conn.Close()
		return nil, nil
	})
	sess = connect(t, s)
	done := make(chan error, 1)
	go func() {
		_, err := sess.client.Definition(context.Background(), "file:///a.go", pos(0, 0))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Definition succeeded after the server died")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending request hung after the server died")
	}
}

func TestStartProcess(t *testing.T) {
	t.Setenv(lspfake.EnvHelper, "sample")
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	c, err := lsp.Start(ctx5(t), []string{os.Args[0]}, root)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	uri := lsp.FileURI(filepath.Join(root, lspfake.SampleFile))
	got, err := c.Definition(ctx5(t), uri, pos(27, 10))
	if err != nil || len(got) != 1 || got[0].Range.Start != pos(17, 5) {
		t.Errorf("Definition via child process = %+v, %v", got, err)
	}
	if err := c.Close(ctx5(t)); err != nil {
		t.Errorf("Close: %v (the child must exit cleanly after shutdown/exit)", err)
	}
}

func TestStartMissingBinary(t *testing.T) {
	_, err := lsp.Start(ctx5(t), []string{"ego-dir-no-such-language-server"}, t.TempDir())
	if err == nil {
		t.Fatal("Start succeeded for a missing binary")
	}
	if !strings.Contains(err.Error(), "ego-dir-no-such-language-server") {
		t.Errorf("error %q should name the missing command", err)
	}
}

func TestServerCommand(t *testing.T) {
	tests := map[string][]string{
		"main.go":   {"gopls"},
		"a/b/x.py":  {"pyright-langserver", "--stdio"},
		"lib.rs":    {"rust-analyzer"},
		"app.ts":    {"typescript-language-server", "--stdio"},
		"app.tsx":   {"typescript-language-server", "--stdio"},
		"app.js":    {"typescript-language-server", "--stdio"},
		"app.jsx":   {"typescript-language-server", "--stdio"},
		"index.php": {"intelephense", "--stdio"},
		"UPPER.GO":  {"gopls"},
	}
	for path, want := range tests {
		got, err := lsp.ServerCommand(path)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ServerCommand(%q) = %v, %v; want %v", path, got, err, want)
		}
	}
	if _, err := lsp.ServerCommand("notes.txt"); err == nil {
		t.Error("ServerCommand(notes.txt) succeeded, want error")
	}
}

func TestLanguageID(t *testing.T) {
	tests := map[string]string{
		"a.go": "go", "a.py": "python", "a.rs": "rust", "a.ts": "typescript",
		"a.tsx": "typescriptreact", "a.js": "javascript", "a.jsx": "javascriptreact", "a.php": "php",
	}
	for path, want := range tests {
		if got := lsp.LanguageID(path); got != want {
			t.Errorf("LanguageID(%q) = %q, want %q", path, got, want)
		}
	}
}

// waitFor polls until the server has received method and returns the first
// such message.
func waitFor(t *testing.T, s *lspfake.Server, method string) lspfake.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if msgs := s.Requests(method); len(msgs) > 0 {
			return msgs[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server never received %s", method)
	return lspfake.Message{}
}

func TestServerRequestReplies(t *testing.T) {
	s := lspfake.New()
	got := make(chan string, 3)
	s.Handle("textDocument/definition", func(ctx context.Context, _ json.RawMessage) (any, error) {
		for _, call := range []struct {
			method string
			params any
		}{
			{"workspace/configuration", map[string]any{"items": []any{map[string]any{}, map[string]any{}}}},
			{"client/registerCapability", map[string]any{"registrations": []any{}}},
			{"window/showMessageRequest", map[string]any{"type": 1, "message": "?"}},
		} {
			res, err := s.Call(ctx, call.method, call.params)
			if err != nil {
				return nil, err
			}
			got <- call.method + "=" + string(res)
		}
		return nil, nil
	})
	sess := connect(t, s)
	if _, err := sess.client.Definition(ctx5(t), "file:///a.go", pos(0, 0)); err != nil {
		t.Fatal(err)
	}
	want := []string{"workspace/configuration=[null,null]", "client/registerCapability=null", "window/showMessageRequest=null"}
	for _, w := range want {
		if g := <-got; g != w {
			t.Errorf("reply %q, want %q", g, w)
		}
	}
}

func TestDocumentSymbolsFlatFallback(t *testing.T) {
	s := lspfake.New()
	r := map[string]any{"start": map[string]int{"line": 3, "character": 5}, "end": map[string]int{"line": 3, "character": 9}}
	s.Result("textDocument/documentSymbol", []any{
		map[string]any{"name": "Rect", "kind": 23, "location": map[string]any{"uri": "file:///a.go", "range": r}},
	})
	sess := connect(t, s)
	syms, err := sess.client.DocumentSymbols(ctx5(t), "file:///a.go")
	want := lsp.Range{Start: pos(3, 5), End: pos(3, 9)}
	if err != nil || len(syms) != 1 || syms[0].Name != "Rect" || syms[0].Range != want || syms[0].SelectionRange != want {
		t.Errorf("DocumentSymbols = %+v, %v", syms, err)
	}
}

func TestMalformedResults(t *testing.T) {
	s := lspfake.New()
	s.Result("textDocument/definition", 42)
	s.Result("textDocument/hover", map[string]any{"contents": 42})
	s.Result("textDocument/documentSymbol", "nope")
	sess := connect(t, s)
	ctx := ctx5(t)
	if _, err := sess.client.Definition(ctx, "file:///a.go", pos(0, 0)); err == nil {
		t.Error("Definition accepted a number")
	}
	if _, err := sess.client.Hover(ctx, "file:///a.go", pos(0, 0)); err == nil {
		t.Error("Hover accepted a number")
	}
	if _, err := sess.client.DocumentSymbols(ctx, "file:///a.go"); err == nil {
		t.Error("DocumentSymbols accepted a string")
	}
}

func TestCloseTwice(t *testing.T) {
	sess := sample(t)
	first := sess.client.Close(ctx5(t))
	if second := sess.client.Close(ctx5(t)); first != nil || second != nil {
		t.Errorf("Close = %v, then %v; want nil both times", first, second)
	}
	if got := sess.server.Requests("shutdown"); len(got) != 1 {
		t.Errorf("shutdown sent %d times, want 1", len(got))
	}
}

func TestStartReportsServerStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	_, err := lsp.Start(ctx5(t), []string{"sh", "-c", "echo 'fatal: no go.mod' >&2; exit 3"}, t.TempDir())
	if err == nil {
		t.Fatal("Start succeeded for a server that exits immediately")
	}
	if !strings.Contains(err.Error(), "fatal: no go.mod") {
		t.Errorf("error %q should include the server's stderr", err)
	}
}

func TestStartEmptyCommand(t *testing.T) {
	if _, err := lsp.Start(ctx5(t), nil, t.TempDir()); err == nil {
		t.Error("Start(nil) succeeded")
	}
}

// specificationServer answers only the specification's spelling of the
// handshake, as gopls, pyright and rust-analyzer do: any other request
// before initialisation is an error.
func specificationServer() *lspfake.Server {
	s := lspfake.New()
	s.Handle("initialise", func(context.Context, json.RawMessage) (any, error) {
		return nil, &lspfake.Error{Code: -32002, Message: "server not initialized"}
	})
	s.Result("initialize", map[string]any{"capabilities": map[string]any{}})
	return s
}

func TestHandshakeFallsBackToSpecificationSpelling(t *testing.T) {
	sess := connect(t, specificationServer())
	if err := sess.client.Close(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	<-sess.done
	want := []string{"initialise", "initialize", "initialized", "shutdown", "exit"}
	if got := sess.server.Methods(); !reflect.DeepEqual(got, want) {
		t.Errorf("methods = %v, want %v", got, want)
	}
	if !sess.server.Requests("initialized")[0].IsNotification() {
		t.Error("initialized must be a notification")
	}
}

func TestHandshakeNotRetriedWhenAccepted(t *testing.T) {
	sess := sample(t)
	if n := len(sess.server.Requests("initialize")); n != 0 {
		t.Errorf("sent initialize %d times to a server that accepted initialise", n)
	}
}

func TestHandshakeBothSpellingsFail(t *testing.T) {
	s := specificationServer()
	s.Handle("initialize", func(context.Context, json.RawMessage) (any, error) {
		return nil, &lspfake.Error{Code: -32603, Message: "no go.mod found"}
	})
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close()
	defer serverEnd.Close()
	go s.Serve(context.Background(), serverEnd)
	_, err := lsp.NewClient(ctx5(t), clientEnd, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "server not initialized") || !strings.Contains(err.Error(), "no go.mod found") {
		t.Errorf("error = %v, want both servers' messages", err)
	}
}

func TestHandshakeNoRetryAfterSessionFailure(t *testing.T) {
	s := lspfake.New()
	var serverEnd net.Conn
	s.Handle("initialise", func(context.Context, json.RawMessage) (any, error) {
		serverEnd.Close()
		return nil, nil
	})
	var clientEnd net.Conn
	clientEnd, serverEnd = net.Pipe()
	defer clientEnd.Close()
	go s.Serve(context.Background(), serverEnd)
	if _, err := lsp.NewClient(ctx5(t), clientEnd, t.TempDir()); err == nil {
		t.Fatal("NewClient succeeded on a dead connection")
	}
	if n := len(s.Requests("initialize")); n != 0 {
		t.Errorf("retried %d times on a dead connection", n)
	}
}
