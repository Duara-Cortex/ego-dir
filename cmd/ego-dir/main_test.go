package main

import (
	"bytes"
	"runtime/debug"
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

func TestResolveVersion(t *testing.T) {
	module := func(v string, settings ...debug.BuildSetting) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/Duara-Cortex/ego-dir", Version: v}, Settings: settings}
	}
	vcs := []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: "daaf907"}, {Key: "vcs.modified", Value: "true"}}
	const pseudo = "v0.0.2-0.20261020120000-daaf907abcde"
	cases := []struct {
		name    string
		ldflags string
		info    *debug.BuildInfo
		ok      bool
		want    string
	}{
		{"ldflags win over build info", "v0.0.1", module("v0.0.9"), true, "v0.0.1"},
		{"ldflags win over a checkout", "v0.0.1", module(pseudo, vcs...), true, "v0.0.1"},
		{"ldflags win without build info", "v0.0.1", nil, false, "v0.0.1"},
		{"module version from go install", "dev", module("v0.0.1"), true, "v0.0.1"},
		{"pseudo-version from go install is shown as is", "dev", module(pseudo), true, pseudo},
		{"empty ldflags fall back to build info", "", module("v0.0.1"), true, "v0.0.1"},
		{"(devel)", "dev", module("(devel)"), true, "dev"},
		{"empty module version", "dev", module(""), true, "dev"},
		{"no build info", "dev", nil, false, "dev"},
		{"nil build info reported as ok", "dev", nil, true, "dev"},
		{"pseudo-version from a checkout", "dev", module(pseudo, vcs...), true, "dev"},
		{"dirty tag from a checkout", "dev", module("v0.0.1+dirty", vcs...), true, "dev"},
		{"exact tag from a checkout", "dev", module("v0.0.1", debug.BuildSetting{Key: "vcs", Value: "git"}), true, "dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.ldflags, c.info, c.ok); got != c.want {
				t.Errorf("resolveVersion(%q, ...) = %q, want %q", c.ldflags, got, c.want)
			}
		})
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
