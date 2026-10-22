// The MCP server keeping language servers warm between calls, against the
// fake language server (see cli_test.go).
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
)

// serverLog names a file the fake language server logs its starts and
// clean exits to, and returns a function that reads them.
func serverLog(t *testing.T) func() (starts, exits int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "servers.log")
	t.Setenv(lspfake.EnvLog, path)
	return func() (starts, exits int) {
		b, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(b), "\n") {
			switch {
			case strings.HasPrefix(line, "start "):
				starts++
			case strings.HasPrefix(line, "exit "):
				exits++
			}
		}
		return starts, exits
	}
}

// copiedSample copies testdata/sample into a temporary directory.
func copiedSample(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "sample")
	if err := os.CopyFS(root, os.DirFS(sampleRoot(t))); err != nil {
		t.Fatal(err)
	}
	return root
}

func edit(t *testing.T, path, old, new string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

var totalAreaUpstream = map[string]any{"file": "shapes.go", "line": 18, "col": 6, "direction": "in"}

// cold is what a fresh CLI run prints for totalAreaUpstream on root.
func cold(t *testing.T, root string) string {
	t.Helper()
	r := cli(t, "inspect_ego", "--root", root, "--file", "shapes.go", "--line", "18", "--col", "6",
		"--direction", "in", "--server", os.Args[0])
	if r.code != 0 {
		t.Fatalf("cli: exit %d: %s", r.code, r.stderr)
	}
	return r.stdout
}

func TestMCPEditsBetweenCallsAreSeen(t *testing.T) {
	servers := serverLog(t)
	root := copiedSample(t)
	s := startMCP(t, "--root", root, "--server", os.Args[0])
	first := s.callTool("inspect_ego", totalAreaUpstream)
	if first.isError || strings.Contains(first.text, "Summary") || first.text != cold(t, root) {
		t.Fatalf("first call (isError %v):\n%s", first.isError, first.text)
	}

	// A new caller in a file the next call does not open first, and a new
	// doc comment in the file it does.
	report := filepath.Join(root, "report", "report.go")
	edit(t, report, "// Label upper-cases a label.", "// Summary is the area of no shapes.\n"+
		"func Summary() float64 { return sample.TotalArea(nil) }\n\n// Label upper-cases a label.")
	edit(t, filepath.Join(root, "shapes.go"), "// TotalArea sums the area of every shape.", "// TotalArea adds up the area of every shape.")

	second := s.callTool("inspect_ego", totalAreaUpstream)
	if second.isError || !strings.Contains(second.text, "Summary") || !strings.Contains(second.text, "adds up") {
		t.Errorf("second call does not show the edits (isError %v):\n%s", second.isError, second.text)
	}
	if want := cold(t, root); second.text != want {
		t.Errorf("warm output differs from a fresh server's\n--- warm ---\n%s\n--- cold ---\n%s", second.text, want)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	// One warm server for both MCP calls, plus the two CLI runs.
	if starts, _ := servers(); starts != 3 {
		t.Errorf("%d servers started, want 1 for MCP and 2 for the CLI", starts)
	}
}

func TestMCPReusesTheServer(t *testing.T) {
	servers := serverLog(t)
	s := sampleMCP(t)
	want := cli(t, append([]string{"inspect_ego"}, at(t, "18", "6", "--depth", "2")...)...).stdout
	for range 3 {
		if got := s.callTool("inspect_ego", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "depth": 2}); got.isError || got.text != want {
			t.Errorf("call = %+v", got)
		}
	}
	if starts, _ := servers(); starts != 2 {
		t.Errorf("%d servers started, want 1 for three MCP calls and 1 for the CLI", starts)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestMCPShutsWarmServersDownOnEOF(t *testing.T) {
	servers := serverLog(t)
	s := sampleMCP(t)
	if got := s.callTool("resolve_definition", map[string]any{"file": "shapes.go", "line": 18, "col": 6}); got.isError {
		t.Fatal(got.text)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if starts, exits := servers(); starts != 1 || exits != 1 {
		t.Errorf("%d started, %d exited cleanly; want the warm server shut down (shutdown, exit)", starts, exits)
	}
	if strings.Contains(s.stderr.String(), "language server") {
		t.Errorf("stderr = %q, want no complaint", s.stderr.String())
	}
}

func TestMCPIdleZeroStartsAServerPerCall(t *testing.T) {
	servers := serverLog(t)
	s := sampleMCP(t, "--idle", "0")
	for range 2 {
		if got := s.callTool("resolve_definition", map[string]any{"file": "shapes.go", "line": 18, "col": 6}); got.isError {
			t.Fatal(got.text)
		}
	}
	if starts, exits := servers(); starts != 2 || exits != 2 {
		t.Errorf("%d started, %d exited; want a server per call", starts, exits)
	}
}

func TestMCPIdleServerIsShutDown(t *testing.T) {
	servers := serverLog(t)
	s := sampleMCP(t, "--idle", "100ms")
	call := func() {
		if got := s.callTool("resolve_definition", map[string]any{"file": "shapes.go", "line": 18, "col": 6}); got.isError {
			t.Fatal(got.text)
		}
	}
	call()
	deadline := time.Now().Add(5 * time.Second)
	for _, exits := servers(); exits == 0 && time.Now().Before(deadline); _, exits = servers() {
		time.Sleep(10 * time.Millisecond)
	}
	if _, exits := servers(); exits != 1 {
		t.Fatal("the idle server was not shut down")
	}
	call()
	if starts, _ := servers(); starts != 2 {
		t.Errorf("%d servers started, want a new one after the idle shutdown", starts)
	}
}

func TestMCPCrashIsAnErrorAndTheNextCallRecovers(t *testing.T) {
	servers := serverLog(t)
	crash := filepath.Join(t.TempDir(), "crash")
	t.Setenv(lspfake.EnvCrashFile, crash)
	s := sampleMCP(t)
	args := map[string]any{"file": "shapes.go", "line": 18, "col": 6}
	if got := s.callTool("resolve_definition", args); got.isError {
		t.Fatal(got.text)
	}
	if err := os.WriteFile(crash, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.callTool("resolve_definition", args); !got.isError || !strings.Contains(got.text, lspfake.CrashNote) {
		t.Errorf("call on the crashing server = %+v, want isError with the server's stderr", got)
	}
	if got := s.callTool("resolve_definition", args); got.isError {
		t.Errorf("call after the crash = %s", got.text)
	}
	if starts, _ := servers(); starts != 2 {
		t.Errorf("%d servers started, want a restart after the crash", starts)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestMCPCancelledCallKeepsTheServer(t *testing.T) {
	servers := serverLog(t)
	t.Setenv(lspfake.EnvLatency, "100ms")
	s := sampleMCP(t)
	s.send(map[string]any{"jsonrpc": "2.0", "id": 50, "method": "tools/call", "params": map[string]any{
		"name": "inspect_ego", "arguments": map[string]any{"file": "shapes.go", "line": 18, "col": 6, "depth": 3}}})
	time.Sleep(300 * time.Millisecond) // the call is talking to its server
	s.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 50}})
	if got := s.callTool("resolve_definition", map[string]any{"file": "shapes.go", "line": 18, "col": 6}); got.isError {
		t.Errorf("call after the cancellation = %s", got.text)
	}
	if starts, exits := servers(); starts != 1 || exits != 0 {
		t.Errorf("%d started, %d exited; want the one server kept", starts, exits)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestMCPIdleUsageErrors(t *testing.T) {
	for _, idle := range []string{"-1s", "soon"} {
		var stdout, stderr strings.Builder
		code := runWithInput([]string{"mcp", "--root", sampleRoot(t), "--idle", idle}, strings.NewReader(""), &stdout, &stderr)
		if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "idle") {
			t.Errorf("--idle %s: exit %d, stdout %q, stderr %q; want 1 and an explanation", idle, code, stdout.String(), stderr.String())
		}
	}
}
