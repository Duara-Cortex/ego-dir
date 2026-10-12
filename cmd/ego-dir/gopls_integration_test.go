//go:build integration

// The CLI against a real gopls. Run with `make integration`.
// It is skipped when gopls is not on PATH.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// realCLI runs the CLI with gopls on the sample workspace.
func realCLI(t *testing.T, args ...string) result {
	t.Helper()
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not on PATH")
	}
	full := append([]string{args[0], "--root", sampleRoot(t), "--file", "shapes.go", "--server", gopls}, args[1:]...)
	var stdout, stderr bytes.Buffer
	code := run(full, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	return result{code, stdout.String(), stderr.String()}
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGoplsMarkdownMatchesGolden(t *testing.T) {
	want := string(readGolden(t, "total_area.golden.md"))
	for i := range 2 { // twice: the output must not depend on the run
		r := realCLI(t, "inspect_ego", "--line", "18", "--col", "6", "--depth", "2")
		if r.stdout != want {
			t.Fatalf("run %d: gopls output differs from the golden file\n--- got ---\n%s\n--- want ---\n%s", i+1, r.stdout, want)
		}
	}
}

func TestGoplsJSONMatchesGolden(t *testing.T) {
	r := realCLI(t, "inspect_ego", "--line", "18", "--col", "6", "--depth", "2", "--format", "json")
	var got, want any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
	}
	if err := json.Unmarshal(readGolden(t, "total_area.golden.json"), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gopls JSON differs from the golden file\n--- got ---\n%s", r.stdout)
	}
}
