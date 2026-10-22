//go:build integration

// The MCP server against a real gopls. Run with `make integration`.
// It is skipped when gopls is not on PATH.
package main

import (
	"os/exec"
	"path/filepath"
	"strings"
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

func TestGoplsMCPEditsBetweenCallsAreSeen(t *testing.T) {
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not on PATH")
	}
	// gopls reports paths with symbolic links resolved.
	root, err := filepath.EvalSymlinks(copiedSample(t))
	if err != nil {
		t.Fatal(err)
	}
	coldGopls := func() string {
		r := cli(t, "inspect_ego", "--root", root, "--file", "shapes.go", "--line", "18", "--col", "6",
			"--direction", "in", "--server", gopls)
		if r.code != 0 {
			t.Fatalf("cli: exit %d: %s", r.code, r.stderr)
		}
		return r.stdout
	}
	s := startMCP(t, "--root", root, "--server", gopls)
	if first := s.callTool("inspect_ego", totalAreaUpstream); first.isError || first.text != coldGopls() {
		t.Fatalf("first call (isError %v):\n%s", first.isError, first.text)
	}
	edit(t, filepath.Join(root, "report", "report.go"), "// Label upper-cases a label.", "// Summary is the area of no shapes.\n"+
		"func Summary() float64 { return sample.TotalArea(nil) }\n\n// Label upper-cases a label.")
	edit(t, filepath.Join(root, "shapes.go"), "// TotalArea sums the area of every shape.", "// TotalArea adds up the area of every shape.")
	second := s.callTool("inspect_ego", totalAreaUpstream)
	if second.isError || !strings.Contains(second.text, "Summary") || !strings.Contains(second.text, "adds up") {
		t.Errorf("second call does not show the edits (isError %v):\n%s", second.isError, second.text)
	}
	if want := coldGopls(); second.text != want {
		t.Errorf("warm gopls output differs from a fresh gopls's\n--- warm ---\n%s\n--- cold ---\n%s", second.text, want)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}
