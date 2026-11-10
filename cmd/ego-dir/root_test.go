// The project root detected from the file when --root is not given.
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/lsp/lspfake"
)

// fakeFS is a set of paths that exist, written with / and made native.
func fakeFS(paths ...string) func(string) bool {
	set := map[string]bool{}
	for _, p := range paths {
		set[filepath.FromSlash(p)] = true
	}
	return func(p string) bool { return set[p] }
}

func TestFindRoot(t *testing.T) {
	goMarkers := []string{"go.mod", "go.work"}
	cases := []struct {
		name    string
		start   string
		markers []string
		fs      func(string) bool
		want    string
	}{
		{"marker in the start directory", "/repo", goMarkers, fakeFS("/repo/go.mod"), "/repo"},
		{"marker above", "/repo/a/b", goMarkers, fakeFS("/repo/go.mod"), "/repo"},
		{"nearest of nested markers wins", "/repo/sub/pkg", goMarkers,
			fakeFS("/repo/go.mod", "/repo/sub/go.mod"), "/repo/sub"},
		{"nearest module wins over a workspace above it", "/ws/mod/pkg", goMarkers,
			fakeFS("/ws/go.work", "/ws/mod/go.mod"), "/ws/mod"},
		{"a workspace with no module below it", "/ws/tools", goMarkers, fakeFS("/ws/go.work"), "/ws"},
		{"second marker of the list", "/p/a", []string{"pyproject.toml", "setup.py"}, fakeFS("/p/setup.py"), "/p"},
		{".git when no marker", "/repo/a", goMarkers, fakeFS("/repo/.git"), "/repo"},
		{"a marker below .git wins", "/repo/a/b", goMarkers, fakeFS("/repo/.git", "/repo/a/go.mod"), "/repo/a"},
		{".git bounds the walk", "/home/me/repo/a", goMarkers, fakeFS("/home/me/go.mod", "/home/me/repo/.git"), "/home/me/repo"},
		{"no markers given: .git only", "/repo/a", nil, fakeFS("/repo/go.mod", "/repo/.git"), "/repo"},
		{"nothing found: the start directory", "/x/y/z", goMarkers, fakeFS(), "/x/y/z"},
		{"start at the filesystem root, nothing found", "/", goMarkers, fakeFS(), "/"},
		{"start at the filesystem root, a marker there", "/", goMarkers, fakeFS("/go.mod"), "/"},
		{"marker at the filesystem root", "/a/b", goMarkers, fakeFS("/go.mod"), "/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, want := filepath.FromSlash(c.start), filepath.FromSlash(c.want)
			if got := findRoot(start, c.markers, c.fs); got != want {
				t.Errorf("findRoot(%q) = %q, want %q", start, got, want)
			}
		})
	}
}

// writeTree creates each path under dir: a directory when it ends in /,
// otherwise an empty file.
func writeTree(t *testing.T, dir string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetectRoot(t *testing.T) {
	// Each tree has a .git at its top, so no walk leaves the temporary
	// directory.
	cases := []struct {
		name string
		tree []string
		file string
		want string
	}{
		{"go.mod", []string{".git/", "m/go.mod"}, "m/a/x.go", "m"},
		{"go.work", []string{".git/", "w/go.work"}, "w/a/x.go", "w"},
		{"pyproject.toml", []string{".git/", "p/pyproject.toml"}, "p/a/x.py", "p"},
		{"setup.py", []string{".git/", "p/setup.py"}, "p/a/x.py", "p"},
		{"setup.cfg", []string{".git/", "p/setup.cfg"}, "p/a/x.py", "p"},
		{"pyrightconfig.json", []string{".git/", "p/pyrightconfig.json"}, "p/a/x.py", "p"},
		{"Cargo.toml", []string{".git/", "c/Cargo.toml"}, "c/src/x.rs", "c"},
		{"package.json for .ts", []string{".git/", "n/package.json"}, "n/src/x.ts", "n"},
		{"package.json for .tsx", []string{".git/", "n/package.json"}, "n/src/x.tsx", "n"},
		{"package.json for .js", []string{".git/", "n/package.json"}, "n/src/x.js", "n"},
		{"package.json for .jsx", []string{".git/", "n/package.json"}, "n/src/x.jsx", "n"},
		{"composer.json", []string{".git/", "h/composer.json"}, "h/src/x.php", "h"},
		{".git directory", []string{".git/"}, "a/b/x.go", "."},
		{".git file (a worktree or submodule)", []string{".git/", "sub/.git"}, "sub/a/x.go", "sub"},
		{"nested modules: the nearest wins", []string{".git/", "go.mod", "testdata/sample/go.mod"},
			"testdata/sample/report/report.go", "testdata/sample"},
		{"a Cargo workspace above a crate", []string{".git/", "Cargo.toml", "crates/c/Cargo.toml"},
			"crates/c/src/x.rs", "crates/c"},
		{"a Python file ignores go.mod", []string{".git/", "a/go.mod"}, "a/b/x.py", "."},
		{"a Go file ignores pyproject.toml", []string{".git/", "a/pyproject.toml"}, "a/b/x.go", "."},
		{"a Go file ignores package.json", []string{".git/", "a/package.json"}, "a/b/x.go", "."},
		{"an unknown extension finds .git only", []string{".git/", "a/go.mod"}, "a/b/x.txt", "."},
		{"a marker in the file's own directory", []string{".git/", "a/go.mod"}, "a/x.go", "a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTree(t, dir, c.tree...)
			writeTree(t, dir, c.file)
			want := filepath.Join(dir, filepath.FromSlash(c.want))
			if got := detectRoot(filepath.Join(dir, filepath.FromSlash(c.file))); got != want {
				t.Errorf("detectRoot(%s) = %q, want %q", c.file, got, want)
			}
		})
	}
}

// TestDetectRootKeepsTheSpelling checks that the root is spelt as the file
// is, whether through a symbolic link (/var or /private/var on macOS) or
// not, so the paths in the output are relative to it either way.
func TestDetectRootKeepsTheSpelling(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, real, ".git/", "mod/go.mod", "mod/a/x.go")
	if err := os.Symlink(filepath.Join(real, "mod"), filepath.Join(real, "link")); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ file, want string }{
		{filepath.Join(dir, "mod", "a", "x.go"), filepath.Join(dir, "mod")},
		{filepath.Join(real, "mod", "a", "x.go"), filepath.Join(real, "mod")},
		// A linked directory is walked by its name: its go.mod is found
		// through the link.
		{filepath.Join(dir, "link", "a", "x.go"), filepath.Join(dir, "link")},
	}
	for _, c := range cases {
		if got := detectRoot(c.file); got != c.want {
			t.Errorf("detectRoot(%s) = %q, want %q", c.file, got, c.want)
		}
	}
}

// validateIn validates a resolve_definition request for file at 1:1.
func validateIn(t *testing.T, root, file string) (*options, error) {
	t.Helper()
	r := newRequest("resolve_definition")
	r.root, r.file, r.line, r.col = root, file, 1, 1
	return r.validate()
}

// samePath compares paths with symbolic links resolved, as the current
// directory may be reported either way.
func samePath(t *testing.T, got, want string) bool {
	t.Helper()
	g, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	return g == w
}

func TestValidateDetectsRootFromRelativeFile(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, ".git/", "mod/go.mod", "mod/a/b/x.go", "mod/a/x.go")
	t.Chdir(filepath.Join(dir, "mod", "a"))
	for _, file := range []string{"b/x.go", "x.go", filepath.Join("..", "a", "x.go")} {
		o, err := validateIn(t, "", file)
		if err != nil {
			t.Fatalf("validate(%s) = %v", file, err)
		}
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(cwd, file); o.path != want {
			t.Errorf("%s: path = %q, want %q (relative to the current directory)", file, o.path, want)
		}
		if !samePath(t, o.root, filepath.Join(dir, "mod")) {
			t.Errorf("%s: root = %q, want the module's directory", file, o.root)
		}
		if !within(o.root, o.path) {
			t.Errorf("%s: root %q does not contain %q by name", file, o.root, o.path)
		}
	}
}

func TestValidateExplicitRootWins(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, ".git/", "mod/go.mod", "mod/a/x.go", "elsewhere/")
	// The current directory plays no part when --root is given.
	t.Chdir(filepath.Join(dir, "elsewhere"))
	o, err := validateIn(t, dir, filepath.Join("mod", "a", "x.go"))
	if err != nil {
		t.Fatal(err)
	}
	if o.root != dir || o.path != filepath.Join(dir, "mod", "a", "x.go") {
		t.Errorf("root, path = %q, %q; want %q and the file under it", o.root, o.path, dir)
	}
	// A relative --root is relative to the current directory.
	if o, err = validateIn(t, filepath.Join("..", "mod"), filepath.Join("a", "x.go")); err != nil {
		t.Fatal(err)
	}
	if !samePath(t, o.root, filepath.Join(dir, "mod")) || !samePath(t, o.path, filepath.Join(dir, "mod", "a", "x.go")) {
		t.Errorf("relative --root: root, path = %q, %q", o.root, o.path)
	}
	// It must still be a directory.
	for _, root := range []string{filepath.Join(dir, "mod", "go.mod"), filepath.Join(dir, "missing")} {
		if _, err := validateIn(t, root, "x.go"); err == nil || !strings.Contains(err.Error(), "is not a directory") {
			t.Errorf("--root %s: err = %v, want not a directory", root, err)
		}
	}
}

func TestValidateDetectedRootMissingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := validateIn(t, "", "nope.go"); err == nil {
		t.Error("a missing file was accepted")
	} else if _, ok := err.(usageError); !ok {
		t.Errorf("err = %T %v, want a usage error", err, err)
	}
}

func TestValidateConfinedNeedsARoot(t *testing.T) {
	r := newRequest("resolve_definition")
	r.file, r.line, r.col, r.confine = filepath.Join(sampleRoot(t), "shapes.go"), 18, 6, true
	if _, err := r.validate(); err == nil || !strings.Contains(err.Error(), "needs a root") {
		t.Errorf("a confined request with no root: err = %v", err)
	}
}

// TestSubdirectoryKeepsNeighboursOutsideIt runs TotalArea from the report
// package's directory. Its callers are in the directory above, which the
// current directory as root would have left out.
func TestSubdirectoryKeepsNeighboursOutsideIt(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("..", "..", "internal", "render", "testdata", "total_area.golden.md"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(sampleRoot(t), "report"))
	r := cli(t, "inspect_ego", "--file", filepath.Join("..", "shapes.go"), "--line", "18", "--col", "6",
		"--depth", "2", "--server", os.Args[0])
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	if r.stdout != string(golden) {
		t.Errorf("output differs from total_area.golden.md\n--- got ---\n%s", r.stdout)
	}
}

// TestSubdirectoryMatchesProjectRoot runs the same position from the
// project root with --root, from it without, from a subdirectory, and from
// outside the project with an absolute --file.
func TestSubdirectoryMatchesProjectRoot(t *testing.T) {
	root := sampleRoot(t)
	cases := []struct {
		name string
		file string // relative to root
		args []string
	}{
		{"module ego", lspfake.SampleReportFile, []string{"inspect_ego", "--line", "2", "--col", "9", "--edges", "all", "--depth", "2", "--format", "json"}},
		{"function in report", lspfake.SampleReportFile, []string{"inspect_ego", "--line", "12", "--col", "6", "--edges", "all", "--direction", "both"}},
		{"type used by report", "shapes.go", []string{"inspect_ego", "--line", "9", "--col", "6", "--edges", "all", "--depth", "2", "--format", "json"}},
		{"references", "shapes.go", []string{"find_references", "--line", "15", "--col", "6", "--format", "json"}},
		{"definition", lspfake.SampleReportFile, []string{"resolve_definition", "--line", "2", "--col", "9"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withRoot := cli(t, append([]string{c.args[0], "--root", root, "--file", c.file, "--server", os.Args[0]}, c.args[1:]...)...)
			if withRoot.code != 0 {
				t.Fatalf("with --root: exit %d, stderr: %s", withRoot.code, withRoot.stderr)
			}
			outside := t.TempDir()
			for _, dir := range []string{".", "report", "outside"} {
				var rel string
				if dir == "outside" { // the file by its absolute path
					t.Chdir(outside)
					rel = filepath.Join(root, filepath.FromSlash(c.file))
				} else {
					t.Chdir(filepath.Join(root, dir))
					var err error
					if rel, err = filepath.Rel(dir, filepath.FromSlash(c.file)); err != nil {
						t.Fatal(err)
					}
				}
				got := cli(t, append([]string{c.args[0], "--file", rel, "--server", os.Args[0]}, c.args[1:]...)...)
				if got.code != 0 || got.stderr != "" {
					t.Fatalf("from %s: exit %d, stderr: %s", dir, got.code, got.stderr)
				}
				if got.stdout != withRoot.stdout {
					t.Errorf("from %s --file %s: output differs from the run with --root\n--- got ---\n%s\n--- want ---\n%s",
						dir, rel, got.stdout, withRoot.stdout)
				}
			}
		})
	}
}

// TestPythonSubdirectoryMatchesGoldens runs every Python golden from the
// project root, the report package's directory and outside the project
// (with an absolute --file), without --root: pyrightconfig.json marks the
// project.
func TestPythonSubdirectoryMatchesGoldens(t *testing.T) {
	t.Setenv(lspfake.EnvHelper, "python")
	goldens := map[string][]byte{} // read before leaving the package directory
	for _, g := range pythonGoldens {
		goldens[g.name] = pythonGolden(t, g.name)
	}
	root, outside := pythonRoot(t), t.TempDir() // the root is found from the current directory
	for _, dir := range []string{".", "report", "outside"} {
		if dir == "outside" {
			t.Chdir(outside)
		} else {
			t.Chdir(filepath.Join(root, dir))
		}
		for _, g := range pythonGoldens {
			t.Run(dir+"/"+g.name, func(t *testing.T) {
				rel := filepath.Join(root, filepath.FromSlash(g.file)) // outside: absolute
				if dir != "outside" {
					var err error
					if rel, err = filepath.Rel(dir, filepath.FromSlash(g.file)); err != nil {
						t.Fatal(err)
					}
				}
				var stdout, stderr bytes.Buffer
				code := run(append([]string{g.args[0], "--file", rel, "--server", os.Args[0]}, g.args[1:]...), &stdout, &stderr)
				if code != 0 || stderr.Len() != 0 {
					t.Fatalf("exit %d, stderr: %s", code, stderr.String())
				}
				if !sameOutput(t, g.name, stdout.String(), goldens[g.name]) {
					t.Errorf("--file %s: output differs from testdata/python/%s\n--- got ---\n%s", rel, g.name, stdout.String())
				}
			})
		}
	}
}

// TestMCPDefaultRootIsWorkingDirectory starts the MCP server in a
// subdirectory of a project without --root: its root, and so what tool
// calls may read, is that directory, not the project above it.
func TestMCPDefaultRootIsWorkingDirectory(t *testing.T) {
	sub := filepath.Join(sampleRoot(t), "report")
	t.Chdir(sub)
	s := startMCP(t, "--server", os.Args[0])
	if !strings.Contains(s.stderr.String(), "for "+sub+"\n") {
		t.Errorf("stderr does not name %s as the root: %q", sub, s.stderr.String())
	}
	for _, file := range []string{filepath.Join("..", "shapes.go"), filepath.Join(sampleRoot(t), "shapes.go")} {
		got := s.callTool("resolve_definition", map[string]any{"file": file, "line": 18, "col": 6})
		if !got.isError || !strings.Contains(got.text, "outside the root") {
			t.Errorf("%s = %+v, want an outside-the-root error", file, got)
		}
	}
	// A file inside it is relative to it. Validation rejects the line
	// before a language server is needed (the fake needs a go.mod at its
	// root, which this directory lacks).
	got := s.callTool("resolve_definition", map[string]any{"file": "report.go", "line": 999, "col": 1})
	if !got.isError || !strings.Contains(got.text, "--line 999 is outside report.go") {
		t.Errorf("report.go = %+v, want a line error from the file in the current directory", got)
	}
	if code := s.close(); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}
