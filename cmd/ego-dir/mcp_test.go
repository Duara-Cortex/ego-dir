// The MCP server end to end, against the fake language server (see
// cli_test.go).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
	"github.com/Duara-Cortex/ego-dir/internal/mcp"
)

// mcpSession is ego-dir mcp running in-process, driven by a scripted client.
type mcpSession struct {
	t      *testing.T
	in     *io.PipeWriter
	lines  chan string
	stderr *syncBuffer
	exit   chan int
	nextID int
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startMCP runs "ego-dir mcp" with args and completes the handshake.
func startMCP(t *testing.T, args ...string) *mcpSession {
	t.Helper()
	s := launchMCP(t, args...)
	res := s.request("initialize", map[string]any{"protocolVersion": mcp.LegacyVersions[0],
		"capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "0"}})
	var init struct {
		ServerInfo struct{ Name, Version string }
	}
	if err := json.Unmarshal(res, &init); err != nil || init.ServerInfo.Name != "ego-dir" || init.ServerInfo.Version != version {
		t.Fatalf("initialize = %s (%v)", res, err)
	}
	s.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return s
}

// launchMCP runs "ego-dir mcp" with args, with no handshake.
func launchMCP(t *testing.T, args ...string) *mcpSession {
	t.Helper()
	t.Setenv(lspfake.EnvHelper, "sample")
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &mcpSession{t: t, in: inW, lines: make(chan string, 16), stderr: &syncBuffer{}, exit: make(chan int, 1)}
	go func() {
		code := runWithInput(append([]string{"mcp"}, args...), inR, outW, s.stderr)
		outW.Close()
		s.exit <- code
	}()
	go func() {
		defer close(s.lines)
		sc := bufio.NewScanner(outR)
		sc.Buffer(nil, 1<<22)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
	}()
	t.Cleanup(func() { inW.Close() })
	return s
}

func (s *mcpSession) send(v any) {
	s.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		s.t.Fatalf("send: %v", err)
	}
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int
		Message string
	} `json:"error"`
}

func (s *mcpSession) recv() rpcReply {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if !ok {
			s.t.Fatalf("stdout closed; stderr: %s", s.stderr.String())
		}
		var r rpcReply
		if err := json.Unmarshal([]byte(line), &r); err != nil || r.JSONRPC != "2.0" {
			s.t.Fatalf("stdout carried a non-protocol line %q", line)
		}
		return r
	case <-time.After(30 * time.Second):
		s.t.Fatal("no reply within 30s")
	}
	return rpcReply{}
}

// request sends a request and returns its result.
func (s *mcpSession) request(method string, params any) json.RawMessage {
	s.t.Helper()
	s.nextID++
	s.send(map[string]any{"jsonrpc": "2.0", "id": s.nextID, "method": method, "params": params})
	r := s.recv()
	if string(r.ID) != fmt.Sprint(s.nextID) || r.Error != nil {
		s.t.Fatalf("%s: reply id %s, error %+v, want a result for id %d", method, r.ID, r.Error, s.nextID)
	}
	return r.Result
}

type callOutcome struct {
	text    string
	isError bool
}

func decodeCall(t *testing.T, raw json.RawMessage) callOutcome {
	t.Helper()
	var res struct {
		Content []struct{ Type, Text string }
		IsError bool
	}
	if err := json.Unmarshal(raw, &res); err != nil || len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("tools/call result = %s (%v)", raw, err)
	}
	return callOutcome{res.Content[0].Text, res.IsError}
}

func (s *mcpSession) callTool(name string, args any) callOutcome {
	s.t.Helper()
	return decodeCall(s.t, s.request("tools/call", map[string]any{"name": name, "arguments": args}))
}

// close ends stdin and returns the exit code.
func (s *mcpSession) close() int {
	s.t.Helper()
	s.in.Close()
	select {
	case code := <-s.exit:
		for line := range s.lines {
			s.t.Errorf("unexpected output after EOF: %s", line)
		}
		return code
	case <-time.After(30 * time.Second):
		s.t.Fatal("ego-dir mcp did not exit after EOF")
	}
	return -1
}

func sampleMCP(t *testing.T, extra ...string) *mcpSession {
	return startMCP(t, append([]string{"--root", sampleRoot(t), "--server", os.Args[0]}, extra...)...)
}

func TestMCPInspectEgoMatchesCLIAndGolden(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "total_area.golden.md"))
	if err != nil {
		t.Fatal(err)
	}
	cliOut := cli(t, append([]string{"inspect_ego"}, at(t, "18", "6", "--depth", "2")...)...)
	if cliOut.code != 0 {
		t.Fatalf("CLI exit %d: %s", cliOut.code, cliOut.stderr)
	}

	s := sampleMCP(t)
	got := s.callTool("inspect_ego", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "depth": 2})
	if got.isError {
		t.Fatalf("isError: %s", got.text)
	}
	if got.text != cliOut.stdout {
		t.Errorf("MCP text differs from the CLI output\n--- MCP ---\n%s\n--- CLI ---\n%s", got.text, cliOut.stdout)
	}
	if got.text != string(golden) {
		t.Errorf("MCP text differs from total_area.golden.md\n%s", got.text)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestMCPToolsMatchCLI(t *testing.T) {
	s := sampleMCP(t)
	cases := []struct {
		tool string
		args map[string]any
		cli  []string
	}{
		{"inspect_ego", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "format": "json", "edges": "calls",
			"direction": "in", "body": "none"},
			at(t, "18", "6", "--format", "json", "--edges", "calls", "--direction", "in", "--body", "none")},
		{"resolve_definition", map[string]any{"file": "shapes.go", "line": 28, "col": 9}, at(t, "28", "9")},
		{"resolve_definition", map[string]any{"file": "shapes.go", "line": 28, "col": 27, "format": "json"},
			at(t, "28", "27", "--format", "json")},
		{"find_references", map[string]any{"file": "shapes.go", "line": 15, "col": 6}, at(t, "15", "6")},
		{"find_references", map[string]any{"file": filepath.Join(sampleRoot(t), "shapes.go"), "line": 28, "col": 42,
			"format": "json"}, at(t, "28", "42", "--format", "json")},
	}
	for _, tc := range cases {
		want := cli(t, append([]string{tc.tool}, tc.cli...)...)
		if want.code != 0 {
			t.Fatalf("CLI %s exit %d: %s", tc.tool, want.code, want.stderr)
		}
		got := s.callTool(tc.tool, tc.args)
		if got.isError || got.text != want.stdout {
			t.Errorf("%s %v:\n--- MCP (isError %v) ---\n%s\n--- CLI ---\n%s", tc.tool, tc.args, got.isError, got.text, want.stdout)
		}
	}
}

func TestMCPToolsList(t *testing.T) {
	s := sampleMCP(t)
	var res struct {
		Tools []struct {
			Name        string
			Description string
			InputSchema struct {
				Type                 string
				Required             []string
				AdditionalProperties *bool
				Properties           map[string]struct {
					Type    string
					Enum    []string
					Default any
				}
			}
			Annotations struct{ ReadOnlyHint bool }
		}
	}
	if err := json.Unmarshal(s.request("tools/list", map[string]any{}), &res); err != nil {
		t.Fatal(err)
	}
	wantProps := map[string]string{
		"inspect_ego":        "body col depth direction edges file format line",
		"resolve_definition": "col file format line",
		"find_references":    "col file format line",
	}
	if len(res.Tools) != len(wantProps) {
		t.Fatalf("got %d tools, want %d", len(res.Tools), len(wantProps))
	}
	for _, tool := range res.Tools {
		sch := tool.InputSchema
		var names []string
		for name := range sch.Properties {
			names = append(names, name)
		}
		slicesSort(names)
		if got := strings.Join(names, " "); got != wantProps[tool.Name] {
			t.Errorf("%s properties = %s, want %s", tool.Name, got, wantProps[tool.Name])
		}
		if sch.Type != "object" || strings.Join(sch.Required, ",") != "file,line,col" ||
			sch.AdditionalProperties == nil || *sch.AdditionalProperties {
			t.Errorf("%s schema = %+v", tool.Name, sch)
		}
		if tool.Description == "" || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s needs a description and readOnlyHint", tool.Name)
		}
		// Defaults are the CLI's.
		d := newRequest(tool.Name)
		if f := sch.Properties["format"]; f.Default != d.format || strings.Join(f.Enum, ",") != "md,json" {
			t.Errorf("%s format = %+v", tool.Name, f)
		}
		if tool.Name == "inspect_ego" {
			p := sch.Properties
			if p["depth"].Default != float64(d.depth) || p["edges"].Default != d.edges ||
				p["direction"].Default != d.direction || p["body"].Default != d.body {
				t.Errorf("inspect_ego defaults = %+v", p)
			}
		}
	}
}

func slicesSort(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func TestMCPRejectsPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(secret, []byte("package secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sub", "dir")); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, secret)
	if err != nil {
		t.Fatal(err)
	}

	s := startMCP(t, "--root", root, "--server", os.Args[0])
	for _, file := range []string{
		rel,                                      // ../<outside>/secret.go
		"sub/../../x.go",                         // climbs out by name
		secret,                                   // absolute, outside
		"link.go",                                // a symbolic link inside the root to a file outside
		filepath.Join("sub", "dir", "secret.go"), // through a linked directory
		"..",
	} {
		for _, tool := range []string{"inspect_ego", "resolve_definition", "find_references"} {
			got := s.callTool(tool, map[string]any{"file": file, "line": 1, "col": 1})
			if !got.isError || !strings.Contains(got.text, "outside the root") {
				t.Errorf("%s %q = %+v, want an outside-the-root error", tool, file, got)
			}
			if strings.Contains(got.text, outside) && !strings.Contains(file, outside) {
				t.Errorf("%s %q: the message names the outside path: %s", tool, file, got.text)
			}
		}
	}
	// A missing file inside the root is reported as missing.
	if got := s.callTool("inspect_ego", map[string]any{"file": "nope.go", "line": 1, "col": 1}); !got.isError ||
		!strings.Contains(got.text, "does not exist") {
		t.Errorf("missing file = %+v", got)
	}
}

func TestMCPAcceptsAbsolutePathInsideRoot(t *testing.T) {
	s := sampleMCP(t)
	got := s.callTool("resolve_definition", map[string]any{"file": filepath.Join(sampleRoot(t), "shapes.go"), "line": 18, "col": 6})
	if got.isError || !strings.Contains(got.text, "- location: shapes.go:18:6") {
		t.Errorf("absolute path inside the root = %+v", got)
	}
}

func TestMCPErrorsAreToolResults(t *testing.T) {
	s := sampleMCP(t)
	cases := map[string]struct {
		tool string
		args any
		want string
	}{
		"line past EOF":          {"inspect_ego", map[string]any{"file": "shapes.go", "line": 999, "col": 1}, "outside shapes.go"},
		"col past EOL":           {"resolve_definition", map[string]any{"file": "shapes.go", "line": 18, "col": 999}, ""},
		"line zero":              {"find_references", map[string]any{"file": "shapes.go", "line": 0, "col": 1}, ""},
		"no symbol at position":  {"inspect_ego", map[string]any{"file": "shapes.go", "line": 2, "col": 1}, ""},
		"bad depth":              {"inspect_ego", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "depth": 6}, "depth"},
		"bad format":             {"inspect_ego", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "format": "xml"}, "format"},
		"bad edges":              {"inspect_ego", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "edges": "likes"}, "likes"},
		"missing arguments":      {"inspect_ego", map[string]any{"file": "shapes.go"}, "line, col"},
		"no arguments":           {"resolve_definition", nil, "file, line, col"},
		"wrong type":             {"inspect_ego", map[string]any{"file": "shapes.go", "line": "18", "col": 6}, "invalid arguments"},
		"root cannot be changed": {"inspect_ego", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "root": "/"}, `unknown field "root"`},
		"server cannot be set":   {"resolve_definition", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "server": "sh"}, `unknown field "server"`},
		"timeout cannot be set":  {"find_references", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "timeout": "1h"}, `unknown field "timeout"`},
		"inspect flag elsewhere": {"resolve_definition", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "depth": 2}, "inspect_ego only"},
		"module references":      {"find_references", map[string]any{"file": "shapes.go", "line": 1, "col": 9}, "module"},
		"unknown extension":      {"inspect_ego", map[string]any{"file": "go.mod", "line": 1, "col": 1}, ""},
	}
	for name, tc := range cases {
		got := s.callTool(tc.tool, tc.args)
		if !got.isError || got.text == "" || !strings.Contains(got.text, tc.want) {
			t.Errorf("%s: %+v, want isError containing %q", name, got, tc.want)
		}
	}
	// The server is still serving.
	if got := s.callTool("resolve_definition", map[string]any{"file": "shapes.go", "line": 18, "col": 6}); got.isError {
		t.Errorf("after errors: %s", got.text)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestMCPTimeoutPerCall(t *testing.T) {
	s := sampleMCP(t, "--timeout", "1ns")
	if got := s.callTool("inspect_ego", map[string]any{"file": "shapes.go", "line": 18, "col": 6}); !got.isError {
		t.Errorf("a 1ns timeout should fail the call, got %s", got.text)
	}
}

func TestMCPConcurrentCalls(t *testing.T) {
	s := sampleMCP(t)
	want := cli(t, append([]string{"inspect_ego"}, at(t, "18", "6", "--depth", "2")...)...).stdout
	const n = 3
	for id := 100; id < 100+n; id++ {
		s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{
			"name": "inspect_ego", "arguments": map[string]any{"file": "shapes.go", "line": 18, "col": 6, "depth": 2}}})
	}
	seen := map[string]bool{}
	for range n {
		r := s.recv()
		seen[string(r.ID)] = true
		if got := decodeCall(t, r.Result); got.isError || got.text != want {
			t.Errorf("call %s = %+v", r.ID, got)
		}
	}
	if len(seen) != n {
		t.Errorf("replies for %v, want %d distinct ids", seen, n)
	}
}

func TestMCPExitsZeroOnEOF(t *testing.T) {
	s := sampleMCP(t)
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(s.stderr.String(), "serving MCP") {
		t.Errorf("stderr = %q, want a startup note", s.stderr.String())
	}
}

func TestMCPUsageErrors(t *testing.T) {
	root := sampleRoot(t)
	cases := map[string][]string{
		"stray argument": {"mcp", "extra"},
		"unknown flag":   {"mcp", "--file", "shapes.go"},
		"zero timeout":   {"mcp", "--timeout", "0s"},
		"root is a file": {"mcp", "--root", filepath.Join(root, "shapes.go")},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			// stdin is never read: the flags fail first.
			code := runWithInput(args, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &stdout, &stderr)
			if code != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
				t.Errorf("exit %d, stdout %q, stderr %q; want 1, nothing, an explanation", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestWithin(t *testing.T) {
	root := filepath.FromSlash("/repo")
	cases := map[string]bool{
		"/repo":             true,
		"/repo/a.go":        true,
		"/repo/..foo":       true,
		"/repo/sub/..x.go":  true,
		"/repo/sub/../a.go": true,
		"/":                 false,
		"/repo/..":          false,
		"/repo/../x.go":     false,
		"/repository/a.go":  false,
		"/elsewhere/a.go":   false,
	}
	for path, want := range cases {
		if got := within(root, filepath.Clean(filepath.FromSlash(path))); got != want {
			t.Errorf("within(%q, %q) = %v, want %v", root, path, got, want)
		}
	}
}

func TestMCPAcceptsResolvedRootPath(t *testing.T) {
	// On macOS t.TempDir is under /var, a link to /private/var; elsewhere the
	// two spellings are the same and this checks the plain case.
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRequest("resolve_definition")
	r.root, r.file, r.line, r.col, r.confine = root, filepath.Join(real, "a.go"), 1, 1, true
	if _, err := r.validate(); err != nil {
		t.Errorf("validate(%s under root %s) = %v", r.file, root, err)
	}
}

// A 2026-07-28 client: it probes with server/discover, then calls a tool,
// each request carrying its version in _meta and no handshake at all.
func TestMCPModernClient(t *testing.T) {
	s := launchMCP(t, "--root", sampleRoot(t), "--server", os.Args[0])
	meta := map[string]any{
		"io.modelcontextprotocol/protocolVersion":    mcp.ModernVersions[0],
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "0"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	var found struct {
		ResultType        string   `json:"resultType"`
		SupportedVersions []string `json:"supportedVersions"`
		Meta              struct {
			ServerInfo struct{ Name, Version string } `json:"io.modelcontextprotocol/serverInfo"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(s.request("server/discover", map[string]any{"_meta": meta}), &found); err != nil {
		t.Fatal(err)
	}
	if found.ResultType != "complete" || len(found.SupportedVersions) == 0 || found.SupportedVersions[0] != mcp.ModernVersions[0] ||
		found.Meta.ServerInfo.Name != "ego-dir" || found.Meta.ServerInfo.Version != version {
		t.Errorf("server/discover = %+v", found)
	}

	golden, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "total_area.golden.md"))
	if err != nil {
		t.Fatal(err)
	}
	raw := s.request("tools/call", map[string]any{"_meta": meta, "name": "inspect_ego",
		"arguments": map[string]any{"file": "shapes.go", "line": 18, "col": 6, "depth": 2}})
	if got := decodeCall(t, raw); got.isError || got.text != string(golden) {
		t.Errorf("modern inspect_ego differs from total_area.golden.md (isError %v)\n%s", got.isError, got.text)
	}
	if !strings.Contains(string(raw), `"resultType":"complete"`) {
		t.Errorf("modern result lacks resultType: %s", raw)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

// The server reports the version that `ego-dir version` prints.
func TestMCPServerVersionMatchesCLI(t *testing.T) {
	if got, want := mcpServer(mcpConfig{}, oneShot{}).Version, currentVersion(); got != want {
		t.Errorf("serverInfo version = %q, want %q", got, want)
	}
}
