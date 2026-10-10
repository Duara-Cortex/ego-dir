//go:build pending

// Acceptance tests for the CLI. The test binary doubles as the language
// server: with lspfake.EnvHelper set it serves the sample scenario, so
// --server can point at os.Args[0].
// Remove the build constraint above once the code under test exists.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rasJacobCharles/ego-dir/internal/lsp/lspfake"
)

func TestMain(m *testing.M) {
	lspfake.RunHelperIfRequested()
	os.Exit(m.Run())
}

type result struct {
	code           int
	stdout, stderr string
}

// cli runs the CLI against the fake language server for the sample workspace.
func cli(t *testing.T, args ...string) result {
	t.Helper()
	t.Setenv(lspfake.EnvHelper, "sample")
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

func sampleRoot(t *testing.T) string {
	t.Helper()
	root, err := lspfake.SampleRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// at returns the common flags for a position in shapes.go.
func at(t *testing.T, line, col string, extra ...string) []string {
	return append([]string{"--root", sampleRoot(t), "--file", "shapes.go", "--line", line, "--col", col, "--server", os.Args[0]}, extra...)
}

type jsonGraph struct {
	Ego   string `json:"ego"`
	Depth int    `json:"depth"`
	Nodes []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Hop  int    `json:"hop"`
		Role string `json:"role"`
		Body string `json:"body"`
	} `json:"nodes"`
	Edges []struct {
		Kind string `json:"kind"`
	} `json:"edges"`
}

func inspectJSON(t *testing.T, extra ...string) jsonGraph {
	t.Helper()
	args := append([]string{"inspect_ego"}, at(t, "18", "6", append([]string{"--format", "json"}, extra...)...)...)
	r := cli(t, args...)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	var g jsonGraph
	if err := json.Unmarshal([]byte(r.stdout), &g); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
	}
	return g
}

func ids(g jsonGraph) []string {
	var out []string
	for _, n := range g.Nodes {
		out = append(out, n.ID)
	}
	return out
}

func TestInspectEgoMarkdownMatchesGolden(t *testing.T) {
	r := cli(t, append([]string{"inspect_ego"}, at(t, "18", "6", "--depth", "2")...)...)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "total_area.golden.md"))
	if err != nil {
		t.Fatal(err)
	}
	if r.stdout != string(want) {
		t.Errorf("stdout differs from the render golden file\n--- got ---\n%s\n--- want ---\n%s", r.stdout, want)
	}
	if r.stderr != "" {
		t.Errorf("stderr should be empty on success, got %q", r.stderr)
	}
}

func TestInspectEgoIsDeterministic(t *testing.T) {
	args := append([]string{"inspect_ego"}, at(t, "18", "6", "--depth", "2")...)
	first := cli(t, args...)
	if first.code != 0 || first.stdout == "" {
		t.Fatalf("exit %d, stdout %q, stderr %s", first.code, first.stdout, first.stderr)
	}
	for i := 0; i < 3; i++ {
		if again := cli(t, args...); again.stdout != first.stdout {
			t.Fatalf("run %d produced different output", i+2)
		}
	}
}

func TestInspectEgoFlags(t *testing.T) {
	g := inspectJSON(t)
	if g.Depth != 1 || g.Ego != "shapes.go:18:6" {
		t.Errorf("default depth/ego = %d/%q, want 1/shapes.go:18:6", g.Depth, g.Ego)
	}
	if got := strings.Join(ids(g), " "); got != "shapes.go:18:6 shapes.go:5:2 shapes.go:27:6" {
		t.Errorf("depth 1 nodes = %s", got)
	}

	g = inspectJSON(t, "--depth", "2", "--edges", "calls")
	if got := strings.Join(ids(g), " "); got != "shapes.go:18:6 shapes.go:5:2 shapes.go:27:6" {
		t.Errorf("--edges calls nodes = %s (the implements hop must be excluded)", got)
	}

	g = inspectJSON(t, "--direction", "in")
	if got := strings.Join(ids(g), " "); got != "shapes.go:18:6 shapes.go:27:6" {
		t.Errorf("--direction in nodes = %s", got)
	}
	g = inspectJSON(t, "--direction", "out", "--depth", "2")
	if got := strings.Join(ids(g), " "); got != "shapes.go:18:6 shapes.go:5:2 shapes.go:12:15" {
		t.Errorf("--direction out nodes = %s", got)
	}

	g = inspectJSON(t, "--body", "all")
	for _, n := range g.Nodes {
		if n.Body == "" {
			t.Errorf("--body all: %s has no body", n.ID)
		}
	}
	g = inspectJSON(t, "--body", "none")
	for _, n := range g.Nodes {
		if n.Body != "" {
			t.Errorf("--body none: %s has a body", n.ID)
		}
	}
}

func TestInspectEgoAbsoluteFileAndCallSite(t *testing.T) {
	root := sampleRoot(t)
	r := cli(t, "inspect_ego", "--root", root, "--file", filepath.Join(root, "shapes.go"),
		"--line", "28", "--col", "9", "--server", os.Args[0], "--format", "json")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, `"ego": "shapes.go:18:6"`) && !strings.Contains(r.stdout, `"ego":"shapes.go:18:6"`) {
		t.Errorf("a call site should resolve to the callee as ego:\n%s", r.stdout)
	}
}

func TestResolveDefinition(t *testing.T) {
	r := cli(t, append([]string{"resolve_definition"}, at(t, "28", "9")...)...)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "shapes.go:18:6") || !strings.Contains(r.stdout, "TotalArea") {
		t.Errorf("resolve_definition output = %q, want TotalArea at shapes.go:18:6", r.stdout)
	}
}

func TestFindReferences(t *testing.T) {
	r := cli(t, append([]string{"find_references"}, at(t, "15", "6")...)...)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	for _, want := range []string{"shapes.go:28:27", "shapes.go:28:42"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("find_references output lacks %s:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, sampleRoot(t)) {
		t.Errorf("output contains an absolute path:\n%s", r.stdout)
	}
}

func TestUsageErrorsExitOne(t *testing.T) {
	root := sampleRoot(t)
	base := func(extra ...string) []string {
		return append([]string{"inspect_ego", "--root", root, "--server", os.Args[0]}, extra...)
	}
	// Control: the same base flags with a valid position must succeed, so
	// these cases fail for the reason named, not because the CLI rejects all.
	if r := cli(t, base("--file", "shapes.go", "--line", "18", "--col", "6")...); r.code != 0 {
		t.Fatalf("control invocation exit %d, stderr %q", r.code, r.stderr)
	}
	cases := map[string][]string{
		"unknown subcommand": {"explode"},
		"missing --file":     base("--line", "18", "--col", "6"),
		"missing --line":     base("--file", "shapes.go", "--col", "6"),
		"missing --col":      base("--file", "shapes.go", "--line", "18"),
		"line zero":          base("--file", "shapes.go", "--line", "0", "--col", "6"),
		"col zero":           base("--file", "shapes.go", "--line", "18", "--col", "0"),
		"line past EOF":      base("--file", "shapes.go", "--line", "999", "--col", "1"),
		"col past EOL":       base("--file", "shapes.go", "--line", "18", "--col", "999"),
		"depth zero":         base("--file", "shapes.go", "--line", "18", "--col", "6", "--depth", "0"),
		"depth six":          base("--file", "shapes.go", "--line", "18", "--col", "6", "--depth", "6"),
		"bad format":         base("--file", "shapes.go", "--line", "18", "--col", "6", "--format", "xml"),
		"bad edge kind":      base("--file", "shapes.go", "--line", "18", "--col", "6", "--edges", "calls,likes"),
		"bad direction":      base("--file", "shapes.go", "--line", "18", "--col", "6", "--direction", "sideways"),
		"bad body mode":      base("--file", "shapes.go", "--line", "18", "--col", "6", "--body", "some"),
		"missing file":       base("--file", "nope.go", "--line", "1", "--col", "1"),
		"unknown extension":  {"inspect_ego", "--root", root, "--file", "go.mod", "--line", "1", "--col", "1"},
		"stray argument":     base("--file", "shapes.go", "--line", "18", "--col", "6", "extra"),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			r := cli(t, args...)
			if r.code != 1 {
				t.Errorf("exit = %d, want 1 (stderr %q)", r.code, r.stderr)
			}
			if r.stdout != "" {
				t.Errorf("stdout should be empty on error, got %q", r.stdout)
			}
			if r.stderr == "" {
				t.Error("stderr should explain the error")
			}
		})
	}
}

func TestRuntimeErrorsExitTwo(t *testing.T) {
	root := sampleRoot(t)
	cases := map[string][]string{
		"server binary missing": {"inspect_ego", "--root", root, "--file", "shapes.go", "--line", "18", "--col", "6",
			"--server", "ego-dir-no-such-language-server"},
		"no symbol at position": append([]string{"inspect_ego"}, at(t, "2", "1")...),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			r := cli(t, args...)
			if r.code != 2 {
				t.Errorf("exit = %d, want 2 (stderr %q)", r.code, r.stderr)
			}
			if r.stdout != "" {
				t.Errorf("stdout should be empty on error, got %q", r.stdout)
			}
			if r.stderr == "" {
				t.Error("stderr should explain the error")
			}
		})
	}
}

func TestVersionStillWorks(t *testing.T) {
	if r := cli(t, "version"); r.code != 0 || !strings.HasPrefix(r.stdout, "ego-dir ") {
		t.Errorf("version = %+v", r)
	}
}
