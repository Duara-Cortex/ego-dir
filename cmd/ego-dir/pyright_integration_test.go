//go:build integration

// The CLI against a real pyright. Run with `make integration`.
// It is skipped when pyright-langserver is not on PATH.
package main

import (
	"bytes"
	"os/exec"
	"testing"
)

// realPyCLI runs the CLI on file in the Python workspace with the default
// server for .py files, pyright-langserver --stdio.
func realPyCLI(t *testing.T, file string, args ...string) result {
	t.Helper()
	if _, err := exec.LookPath("pyright-langserver"); err != nil {
		t.Skip("pyright-langserver not on PATH")
	}
	full := append([]string{args[0], "--root", pythonRoot(t), "--file", file}, args[1:]...)
	var stdout, stderr bytes.Buffer
	code := run(full, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	return result{code, stdout.String(), stderr.String()}
}

// TestPyrightMatchesGoldens requires pyright to produce the Python golden
// files, as the fake does, on two runs.
func TestPyrightMatchesGoldens(t *testing.T) {
	for _, g := range pythonGoldens {
		t.Run(g.name, func(t *testing.T) {
			want := pythonGolden(t, g.name)
			for i := range 2 {
				r := realPyCLI(t, g.file, g.args...)
				if !sameOutput(t, g.name, r.stdout, want) {
					t.Fatalf("run %d: pyright output differs from testdata/python/%s\n--- got ---\n%s\n--- want ---\n%s", i+1, g.name, r.stdout, want)
				}
			}
		})
	}
}
