// CLI acceptance tests for modules: the ego on a package clause, its
// imports and members, against the fake language server.
package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
)

// atFile returns the common flags for a position in file (relative to the
// sample root).
func atFile(t *testing.T, file, line, col string, extra ...string) []string {
	return append([]string{"--root", sampleRoot(t), "--file", file, "--line", line, "--col", col, "--server", os.Args[0]}, extra...)
}

func TestInspectModuleMarkdown(t *testing.T) {
	r := cli(t, append([]string{"inspect_ego"}, atFile(t, lspfake.SampleReportFile, "2", "9")...)...)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	for _, want := range []string{
		"# Ego: `report`\n\n- kind: module\n- location: example.com/sample/report\n",
		"## Doc\n\nPackage report describes the sample's shapes.\n",
		// imports is a default edge kind; contains is not.
		"## Members\n\n_none_\n",
		"## Type dependencies\n\n- `fmt` (module) fmt\n- `strings` (module) strings\n- `sample` (module) example.com/sample\n  - signature: `package sample`\n",
		"| `report` example.com/sample/report | imports | `sample` example.com/sample | 1 |\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("output lacks %q\n--- got ---\n%s", want, r.stdout)
		}
	}
}

func TestInspectModuleJSON(t *testing.T) {
	r := cli(t, append([]string{"inspect_ego"}, atFile(t, lspfake.SampleReportFile, "2", "9", "--edges", "imports,contains", "--format", "json")...)...)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	var g jsonGraph
	if err := json.Unmarshal([]byte(r.stdout), &g); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"module:example.com/sample/report",
		"module:fmt", "module:strings", "module:example.com/sample",
		"report/report.go:12:6", "report/report.go:17:6",
	}
	if got := strings.Join(ids(g), " "); got != strings.Join(want, " ") {
		t.Errorf("ids = %s\nwant %s", got, strings.Join(want, " "))
	}
	if g.Ego != "module:example.com/sample/report" {
		t.Errorf("ego = %q", g.Ego)
	}
}

func TestInspectModuleIsDeterministic(t *testing.T) {
	args := append([]string{"inspect_ego"}, atFile(t, lspfake.SampleReportFile, "2", "9", "--edges", "all", "--depth", "3", "--body", "all")...)
	first := cli(t, args...)
	second := cli(t, args...)
	if first.code != 0 || first.stdout != second.stdout {
		t.Errorf("runs differ (exit %d):\n--- first ---\n%s\n--- second ---\n%s", first.code, first.stdout, second.stdout)
	}
	if strings.Contains(first.stdout, sampleRoot(t)) {
		t.Errorf("output contains an absolute path:\n%s", first.stdout)
	}
}

func TestMembersAreOptIn(t *testing.T) {
	r := cli(t, append([]string{"inspect_ego"}, at(t, "9", "6", "--edges", "contains")...)...)
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	want := "## Members\n\n- parent: `sample` (module) example.com/sample\n  - signature: `package sample`\n" +
		"- `W` (field) shapes.go:9\n  - signature: `field W float64`\n" +
		"- `H` (field) shapes.go:9\n  - signature: `field H float64`\n" +
		"- `Area` (method) shapes.go:12\n"
	if !strings.Contains(r.stdout, want) {
		t.Errorf("output lacks %q\n--- got ---\n%s", want, r.stdout)
	}
}

func TestResolveDefinitionOfAModule(t *testing.T) {
	r := cli(t, append([]string{"resolve_definition"}, atFile(t, lspfake.SampleReportFile, "2", "9")...)...)
	if r.code != 0 || !strings.Contains(r.stdout, "- location: module:example.com/sample/report\n- declaration: example.com/sample/report\n") {
		t.Errorf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestFindReferencesOfAModuleIsAnError(t *testing.T) {
	r := cli(t, append([]string{"find_references"}, atFile(t, lspfake.SampleReportFile, "2", "9")...)...)
	if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "module") {
		t.Errorf("exit %d, stdout %q, stderr %q; want 2 and a message about modules", r.code, r.stdout, r.stderr)
	}
}
