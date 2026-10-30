// CLI acceptance tests for Python, against the fake language server
// answering like pyright 1.1.414 for testdata/python. The golden files in
// testdata/python are what real pyright must produce too (see
// pyright_integration_test.go).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
)

var update = flag.Bool("update", false, "rewrite the Python golden files with the fake's output")

func pythonRoot(t *testing.T) string {
	t.Helper()
	root, err := lspfake.PythonRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// pyCLI runs the CLI against the fake pyright on file in the Python
// workspace. args are the command, then its flags without --root, --file
// and --server.
func pyCLI(t *testing.T, file string, args ...string) result {
	t.Helper()
	t.Setenv(lspfake.EnvHelper, "python")
	full := append([]string{args[0], "--root", pythonRoot(t), "--file", file, "--server", os.Args[0]}, args[1:]...)
	var stdout, stderr bytes.Buffer
	code := run(full, &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

// pythonGoldens are the Python golden files and the runs that produce them.
var pythonGoldens = []struct {
	name string
	file string
	args []string
}{
	{"total_area.golden.md", lspfake.PythonFile, []string{"inspect_ego", "--line", "31", "--col", "5", "--depth", "2"}},
	{"total_area.golden.json", lspfake.PythonFile, []string{"inspect_ego", "--line", "31", "--col", "5", "--depth", "2", "--format", "json"}},
	{"rect.golden.md", lspfake.PythonFile, []string{"inspect_ego", "--line", "14", "--col", "7", "--edges", "all", "--body", "all"}},
	{"report_module.golden.md", lspfake.PythonReportFile, []string{"inspect_ego", "--line", "1", "--col", "1", "--edges", "all", "--depth", "2"}},
	{"rect_references.golden.md", lspfake.PythonFile, []string{"find_references", "--line", "14", "--col", "7"}},
}

func pythonGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "python", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameOutput compares output with a golden file: Markdown byte for byte,
// JSON by value.
func sameOutput(t *testing.T, name, got string, want []byte) bool {
	t.Helper()
	if !strings.HasSuffix(name, ".json") {
		return got == string(want)
	}
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(g, w)
}

func TestPythonMatchesGoldens(t *testing.T) {
	for _, g := range pythonGoldens {
		t.Run(g.name, func(t *testing.T) {
			r := pyCLI(t, g.file, g.args...)
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
			}
			if *update {
				if err := os.WriteFile(filepath.Join("testdata", "python", g.name), []byte(r.stdout), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if !sameOutput(t, g.name, r.stdout, pythonGolden(t, g.name)) {
				t.Errorf("output differs from testdata/python/%s\n--- got ---\n%s", g.name, r.stdout)
			}
			if again := pyCLI(t, g.file, g.args...); again.stdout != r.stdout {
				t.Error("a second run gave different output")
			}
			if strings.Contains(r.stdout, pythonRoot(t)) || strings.Contains(r.stdout, "lspfake-pyright") {
				t.Errorf("output names a path outside the workspace or an absolute one:\n%s", r.stdout)
			}
		})
	}
}

func TestPythonResolveDefinitionFromCallSite(t *testing.T) {
	r := pyCLI(t, lspfake.PythonFile, "resolve_definition", "--line", "41", "--col", "24", "--format", "json")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	var out struct {
		Symbol struct{ ID, Name, Signature, Doc string } `json:"symbol"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatal(err)
	}
	if out.Symbol.ID != "shapes.py:26:5" || out.Symbol.Name != "new_rect" ||
		out.Symbol.Signature != "def new_rect(w: float, h: float) -> Rect" || out.Symbol.Doc != "Build a Rect, like new_rect(1, 2)." {
		t.Errorf("resolve_definition = %+v", out.Symbol)
	}
}

func TestPythonModuleHasNoReferences(t *testing.T) {
	if r := pyCLI(t, lspfake.PythonReportFile, "find_references", "--line", "1", "--col", "1"); r.code != 2 ||
		!strings.Contains(r.stderr, "report.report is a module") {
		t.Errorf("find_references on a module = exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestPythonNoSymbolIsARuntimeError(t *testing.T) {
	// Line 2 of shapes.py is blank.
	if r := pyCLI(t, lspfake.PythonFile, "inspect_ego", "--line", "2", "--col", "1"); r.code != 2 || r.stdout != "" {
		t.Errorf("exit %d, stdout %q, stderr %q; want 2 and no output", r.code, r.stdout, r.stderr)
	}
}
