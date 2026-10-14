//go:build integration

// The MCP server against a real gopls. Run with `make integration`.
// It is skipped when gopls is not on PATH.
package main

import (
	"os/exec"
	"testing"
)

func TestGoplsMCPInspectEgoMatchesGolden(t *testing.T) {
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not on PATH")
	}
	s := startMCP(t, "--root", sampleRoot(t), "--server", gopls)
	got := s.callTool("inspect_ego", map[string]any{"file": "shapes.go", "line": 18, "col": 6, "depth": 2})
	if want := string(readGolden(t, "total_area.golden.md")); got.isError || got.text != want {
		t.Errorf("gopls over MCP differs from the golden file (isError %v)\n--- got ---\n%s\n--- want ---\n%s", got.isError, got.text, want)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}
