package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// client is a scripted MCP client talking to a Server over pipes.
type client struct {
	t      *testing.T
	in     *io.PipeWriter // the server's stdin
	out    chan reply     // the server's stdout, a message at a time
	served chan error     // Serve's result
}

type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	raw string
}

func start(t *testing.T, s *Server) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, out: make(chan reply, 64), served: make(chan error, 1)}
	go func() {
		err := s.Serve(context.Background(), inR, outW)
		outW.Close()
		c.served <- err
	}()
	go func() {
		defer close(c.out)
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var r reply
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				r.raw = "not JSON: " + sc.Text()
			} else {
				r.raw = sc.Text()
			}
			c.out <- r
		}
	}()
	t.Cleanup(func() { inW.Close() })
	return c
}

func (c *client) send(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

func (c *client) recv() reply {
	c.t.Helper()
	select {
	case r, ok := <-c.out:
		if !ok {
			c.t.Fatal("server closed stdout")
		}
		if strings.HasPrefix(r.raw, "not JSON") {
			c.t.Fatal(r.raw)
		}
		if r.JSONRPC != "2.0" {
			c.t.Fatalf("reply without jsonrpc 2.0: %s", r.raw)
		}
		return r
	case <-time.After(5 * time.Second):
		c.t.Fatal("no reply within 5s")
	}
	return reply{}
}

// call sends a request and returns its reply, which must be the next message.
func (c *client) call(id int, method, params string) reply {
	c.t.Helper()
	if params == "" {
		c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q}`, id, method))
	} else {
		c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))
	}
	r := c.recv()
	if string(r.ID) != fmt.Sprint(id) {
		c.t.Fatalf("reply id = %s, want %d: %s", r.ID, id, r.raw)
	}
	return r
}

// close ends the server's stdin and returns everything written after it,
// once Serve has returned.
func (c *client) close() []reply {
	c.t.Helper()
	c.in.Close()
	select {
	case err := <-c.served:
		if err != nil {
			c.t.Errorf("Serve = %v, want nil at EOF", err)
		}
	case <-time.After(5 * time.Second):
		c.t.Fatal("Serve did not return after EOF")
	}
	var rest []reply
	for r := range c.out {
		rest = append(rest, r)
	}
	return rest
}

func wantError(t *testing.T, r reply, code int) {
	t.Helper()
	if r.Error == nil || r.Error.Code != code || r.Result != nil {
		t.Fatalf("want error %d, got %s", code, r.raw)
	}
}

type toolResult struct {
	Content []struct{ Type, Text string } `json:"content"`
	IsError bool                          `json:"isError"`
}

func resultOf(t *testing.T, r reply) toolResult {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("tools/call failed: %s", r.raw)
	}
	var res toolResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("content = %+v, want one text item", res.Content)
	}
	return res
}

var schema = json.RawMessage(`{"type":"object","properties":{"word":{"type":"string"}}}`)

// echo returns its "word" argument, or an error if it is missing.
var echo = Tool{
	Name: "echo", Title: "Echo", Description: "Repeats a word.", InputSchema: schema,
	Handler: func(_ context.Context, args json.RawMessage) (string, error) {
		var a struct{ Word string }
		if args != nil {
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
		}
		if a.Word == "" {
			return "", errors.New("word is required")
		}
		return a.Word, nil
	},
}

func newServer(tools ...Tool) *Server {
	return &Server{Name: "test", Version: "v1.2.3", Tools: tools}
}

func TestInitializeNegotiatesVersion(t *testing.T) {
	cases := map[string]string{"1999-01-01": LegacyVersions[0]}
	for _, v := range LegacyVersions {
		cases[v] = v
	}
	for asked, want := range cases {
		t.Run(asked, func(t *testing.T) {
			c := start(t, newServer(echo))
			r := c.call(1, "initialize", fmt.Sprintf(
				`{"protocolVersion":%q,"capabilities":{"roots":{}},"clientInfo":{"name":"scripted","version":"0"}}`, asked))
			var res struct {
				ProtocolVersion string                     `json:"protocolVersion"`
				Capabilities    map[string]json.RawMessage `json:"capabilities"`
				ServerInfo      struct{ Name, Version string }
			}
			if err := json.Unmarshal(r.Result, &res); err != nil {
				t.Fatalf("%v: %s", err, r.raw)
			}
			if res.ProtocolVersion != want {
				t.Errorf("protocolVersion = %q, want %q", res.ProtocolVersion, want)
			}
			if len(res.Capabilities) != 1 || string(res.Capabilities["tools"]) != "{}" {
				t.Errorf("capabilities = %s, want only tools: {}", r.Result)
			}
			if res.ServerInfo.Name != "test" || res.ServerInfo.Version != "v1.2.3" {
				t.Errorf("serverInfo = %+v", res.ServerInfo)
			}
			c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
			c.call(2, "ping", "")
			if rest := c.close(); len(rest) != 0 {
				t.Errorf("unexpected messages: %v", rest)
			}
		})
	}
}

func TestInitializeNeedsAVersion(t *testing.T) {
	c := start(t, newServer())
	wantError(t, c.call(1, "initialize", `{"capabilities":{}}`), CodeInvalidParams)
	wantError(t, c.call(2, "initialize", ""), CodeInvalidParams)
}

func TestPing(t *testing.T) {
	c := start(t, newServer())
	if r := c.call(1, "ping", ""); string(r.Result) != "{}" {
		t.Errorf("ping result = %s, want {}", r.raw)
	}
	// String ids are echoed as strings.
	c.send(`{"jsonrpc":"2.0","id":"abc","method":"ping"}`)
	if r := c.recv(); string(r.ID) != `"abc"` {
		t.Errorf("id = %s, want \"abc\"", r.ID)
	}
}

func TestToolsList(t *testing.T) {
	c := start(t, newServer(echo))
	r := c.call(1, "tools/list", `{}`)
	var res struct {
		Tools []struct {
			Name, Title, Description string
			InputSchema              json.RawMessage
			Annotations              map[string]bool
		}
		NextCursor *string
	}
	if err := json.Unmarshal(r.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.NextCursor != nil {
		t.Fatalf("tools/list = %s", r.raw)
	}
	got := res.Tools[0]
	if got.Name != "echo" || got.Title != "Echo" || got.Description != "Repeats a word." || string(got.InputSchema) != string(schema) {
		t.Errorf("tool = %+v", got)
	}
	want := map[string]bool{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	for k, v := range want {
		if b, ok := got.Annotations[k]; !ok || b != v {
			t.Errorf("annotation %s = %v (set %v), want %v", k, b, ok, v)
		}
	}
	// A cursor (pagination) is accepted; the whole list fits one page.
	if r := c.call(2, "tools/list", `{"cursor":"x"}`); r.Error != nil {
		t.Errorf("tools/list with a cursor: %s", r.raw)
	}
}

func TestToolsCall(t *testing.T) {
	c := start(t, newServer(echo))

	res := resultOf(t, c.call(1, "tools/call", `{"name":"echo","arguments":{"word":"hello"},"_meta":{"progressToken":1}}`))
	if res.IsError || res.Content[0].Text != "hello" {
		t.Errorf("success = %+v", res)
	}
	if r := c.call(2, "tools/call", `{"name":"echo","arguments":{"word":"hi"}}`); !strings.Contains(r.raw, `"isError":false`) {
		t.Errorf("isError should be sent as false on success: %s", r.raw)
	}

	// Tool errors are results the model can read, not protocol errors.
	for id, params := range map[int]string{
		3: `{"name":"echo","arguments":{}}`,
		4: `{"name":"echo"}`,
		5: `{"name":"echo","arguments":null}`,
	} {
		res := resultOf(t, c.call(id, "tools/call", params))
		if !res.IsError || res.Content[0].Text != "word is required" {
			t.Errorf("%s = %+v, want isError with the message", params, res)
		}
	}

	wantError(t, c.call(6, "tools/call", `{"name":"nope","arguments":{}}`), CodeInvalidParams)
	wantError(t, c.call(7, "tools/call", `{"arguments":{}}`), CodeInvalidParams)
	wantError(t, c.call(8, "tools/call", ""), CodeInvalidParams)
}

func TestToolPanicIsAToolError(t *testing.T) {
	boom := Tool{Name: "boom", InputSchema: schema, Handler: func(context.Context, json.RawMessage) (string, error) {
		panic("kaboom")
	}}
	c := start(t, newServer(boom))
	res := resultOf(t, c.call(1, "tools/call", `{"name":"boom"}`))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "kaboom") {
		t.Errorf("panic = %+v", res)
	}
	c.call(2, "ping", "")
}

func TestUnknownMethod(t *testing.T) {
	c := start(t, newServer())
	r := c.call(1, "resources/list", "")
	wantError(t, r, CodeMethodNotFound)
	if !strings.Contains(r.Error.Message, "resources/list") {
		t.Errorf("message = %q", r.Error.Message)
	}
}

func TestMalformedInput(t *testing.T) {
	c := start(t, newServer())
	cases := []struct {
		line string
		code int
	}{
		{`{"jsonrpc":"2.0","id":1,"method":`, CodeParseError},
		{`not json`, CodeParseError},
		{`42`, CodeInvalidRequest},
		{`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, CodeInvalidRequest},
		{`{"jsonrpc":"2.0","id":1}`, CodeInvalidRequest},
	}
	for _, tc := range cases {
		c.send(tc.line)
		r := c.recv()
		wantError(t, r, tc.code)
		if tc.code != CodeInvalidRequest || tc.line[0] != '{' {
			if string(r.ID) != "null" {
				t.Errorf("%s: id = %s, want null", tc.line, r.ID)
			}
		}
	}
	// A request without "jsonrpc": "2.0" is invalid but keeps its id.
	c.send(`{"id":9,"method":"ping"}`)
	if r := c.recv(); string(r.ID) != "9" {
		t.Errorf("id = %s, want 9", r.ID)
	} else {
		wantError(t, r, CodeInvalidRequest)
	}
	// The server keeps going.
	c.call(2, "ping", "")
}

func TestNotificationsGetNoReply(t *testing.T) {
	c := start(t, newServer(echo))
	for _, n := range []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/roots/list_changed","params":{}}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo","arguments":{"word":"x"}}}`,
		`{"jsonrpc":"2.0","method":"ping","id":null}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled"}`,
		`{"method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":5,"result":{}}`, // a response from the client
	} {
		c.send(n)
	}
	// The ping's reply is the very next message.
	c.call(1, "ping", "")
	if rest := c.close(); len(rest) != 0 {
		t.Errorf("unexpected messages: %v", rest)
	}
}

func TestLineFraming(t *testing.T) {
	c := start(t, newServer())
	c.send("")
	c.send("   ")
	c.send(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\r")
	if r := c.recv(); string(r.ID) != "1" || r.Error != nil {
		t.Errorf("CRLF line: %s", r.raw)
	}
	// The last message needs no newline before EOF.
	if _, err := io.WriteString(c.in, `{"jsonrpc":"2.0","id":2,"method":"ping"}`); err != nil {
		t.Fatal(err)
	}
	rest := c.close()
	if len(rest) != 1 || string(rest[0].ID) != "2" {
		t.Errorf("after EOF = %v, want the reply to id 2", rest)
	}
}

// blocker is a tool that runs until released or cancelled, and records
// what it saw.
type blocker struct {
	started  chan string
	release  chan struct{}
	ended    chan error
	running  atomic.Int32
	mostSeen atomic.Int32
}

func newBlocker() *blocker {
	return &blocker{started: make(chan string, 16), release: make(chan struct{}), ended: make(chan error, 16)}
}

func (b *blocker) tool() Tool {
	return Tool{Name: "block", InputSchema: schema, Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
		n := b.running.Add(1)
		defer b.running.Add(-1)
		for {
			m := b.mostSeen.Load()
			if n <= m || b.mostSeen.CompareAndSwap(m, n) {
				break
			}
		}
		b.started <- string(args)
		select {
		case <-b.release:
			b.ended <- nil
			return "released " + string(args), nil
		case <-ctx.Done():
			b.ended <- ctx.Err()
			return "", ctx.Err()
		}
	}}
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

func TestCancellation(t *testing.T) {
	b := newBlocker()
	c := start(t, newServer(b.tool()))
	c.send(`{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"block","arguments":{}}}`)
	waitFor(t, b.started, "the call to start")

	// The server still answers while the call runs.
	c.call(1, "ping", "")

	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"call-1","reason":"user"}}`)
	if err := waitFor(t, b.ended, "the call to end"); !errors.Is(err, context.Canceled) {
		t.Fatalf("handler ended with %v, want context.Canceled", err)
	}
	c.call(2, "ping", "")
	// A cancelled request gets no reply.
	if rest := c.close(); len(rest) != 0 {
		t.Errorf("unexpected messages: %v", rest)
	}
}

func TestCancellingOneCallLeavesOthers(t *testing.T) {
	b := newBlocker()
	c := start(t, newServer(b.tool()))
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{"n":1}}}`)
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"block","arguments":{"n":2}}}`)
	waitFor(t, b.started, "call 1")
	waitFor(t, b.started, "call 2")
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	waitFor(t, b.ended, "the cancelled call")
	close(b.release)
	r := c.recv()
	if string(r.ID) != "2" {
		t.Fatalf("reply = %s, want id 2 only", r.raw)
	}
	if res := resultOf(t, r); res.IsError || res.Content[0].Text != `released {"n":2}` {
		t.Errorf("call 2 = %+v", res)
	}
}

func TestEOFCancelsRunningCalls(t *testing.T) {
	b := newBlocker()
	c := start(t, newServer(b.tool()))
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{}}}`)
	waitFor(t, b.started, "the call to start")
	if rest := c.close(); len(rest) != 0 {
		t.Errorf("unexpected messages after EOF: %v", rest)
	}
	// close waited for Serve, which waits for its calls.
	select {
	case err := <-b.ended:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("handler ended with %v, want context.Canceled", err)
		}
	default:
		t.Error("Serve returned before its call ended")
	}
}

func TestConcurrentCallsAreBounded(t *testing.T) {
	b := newBlocker()
	s := newServer(b.tool())
	s.MaxConcurrent = 2
	c := start(t, s)
	for id := 1; id <= 3; id++ {
		c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"block","arguments":{"n":%d}}}`, id, id))
	}
	waitFor(t, b.started, "a first call")
	waitFor(t, b.started, "a second call")
	select {
	case args := <-b.started:
		t.Fatalf("a third call (%s) started while two were running", args)
	case <-time.After(100 * time.Millisecond):
	}
	c.call(4, "ping", "") // not held up by the queue

	close(b.release)
	waitFor(t, b.started, "the third call")
	got := map[string]string{}
	for range 3 {
		r := c.recv()
		got[string(r.ID)] = resultOf(t, r).Content[0].Text
	}
	for id := 1; id <= 3; id++ {
		if want := fmt.Sprintf(`released {"n":%d}`, id); got[fmt.Sprint(id)] != want {
			t.Errorf("call %d = %q, want %q", id, got[fmt.Sprint(id)], want)
		}
	}
	if m := b.mostSeen.Load(); m != 2 {
		t.Errorf("at most %d calls ran at once, want 2", m)
	}
}

func TestQueuedCallCanBeCancelled(t *testing.T) {
	b := newBlocker()
	s := newServer(b.tool())
	s.MaxConcurrent = 1
	c := start(t, s)
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{}}}`)
	waitFor(t, b.started, "the first call")
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"block","arguments":{}}}`)
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2}}`)
	c.call(3, "ping", "")
	close(b.release)
	if r := c.recv(); string(r.ID) != "1" {
		t.Fatalf("reply = %s, want id 1", r.raw)
	}
	if rest := c.close(); len(rest) != 0 {
		t.Errorf("unexpected messages: %v", rest)
	}
	select {
	case args := <-b.started:
		t.Errorf("the cancelled call ran (%s)", args)
	default:
	}
}

func TestDuplicateInFlightID(t *testing.T) {
	b := newBlocker()
	c := start(t, newServer(b.tool()))
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{}}}`)
	waitFor(t, b.started, "the call to start")
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"block","arguments":{}}}`)
	r := c.recv()
	wantError(t, r, CodeInvalidRequest)
	if string(r.ID) != "null" {
		t.Errorf("id = %s, want null", r.ID)
	}
	close(b.release)
	if r := c.recv(); string(r.ID) != "1" || resultOf(t, r).IsError {
		t.Errorf("first call = %s", r.raw)
	}
}

func TestConcurrentCallsUnderRace(t *testing.T) {
	c := start(t, newServer(echo))
	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"echo","arguments":{"word":"w%d"}}}`, i, i))
		}()
	}
	wg.Wait()
	for range n {
		r := c.recv()
		if want := "w" + string(r.ID); resultOf(t, r).Content[0].Text != want {
			t.Errorf("reply %s = %s, want %s", r.ID, r.raw, want)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestWriteFailureEndsServe(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
	err := newServer().Serve(context.Background(), in, failingWriter{})
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Errorf("Serve = %v, want the write error", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

func TestReadFailureEndsServe(t *testing.T) {
	err := newServer().Serve(context.Background(), failingReader{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("Serve = %v, want the read error", err)
	}
}
