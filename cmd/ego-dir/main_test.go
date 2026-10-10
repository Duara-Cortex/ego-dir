package main

import (
	"bytes"
	"testing"
)

func TestRunVersion(t *testing.T) {
	for _, arg := range []string{"version", "--version"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{arg}, &stdout, &stderr); code != 0 {
			t.Fatalf("run(%q) exit = %d, want 0", arg, code)
		}
		if got, want := stdout.String(), "ego-dir dev\n"; got != want {
			t.Errorf("run(%q) stdout = %q, want %q", arg, got, want)
		}
		if stderr.Len() != 0 {
			t.Errorf("run(%q) wrote to stderr: %q", arg, stderr.String())
		}
	}
}

func TestRunUsageError(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"version", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 1 {
			t.Errorf("run(%q) exit = %d, want 1", args, code)
		}
		if stdout.Len() != 0 {
			t.Errorf("run(%q) wrote to stdout: %q", args, stdout.String())
		}
		if stderr.Len() == 0 {
			t.Errorf("run(%q) wrote no usage to stderr", args)
		}
	}
}
