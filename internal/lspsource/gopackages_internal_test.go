package lspsource

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// writeTree creates files (path relative to dir: content) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func lspRange(line, start, end int) lsp.Range {
	return lsp.Range{Start: lsp.Position{Line: line, Character: start}, End: lsp.Position{Line: line, Character: end}}
}

func TestModuleOfSample(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "sample"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok, err := golang.ModuleOf(filepath.Join(root, "report", "report.go"))
	if err != nil || !ok {
		t.Fatalf("ModuleOf = %v, %v", ok, err)
	}
	want := Module{
		Path: "example.com/sample/report", Name: "report", Dir: filepath.Join(root, "report"),
		Files: []ModuleFile{{Path: filepath.Join(root, "report", "report.go"), Header: lspRange(1, 8, 14)}},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("ModuleOf =\n %+v\nwant\n %+v", m, want)
	}

	imports, err := golang.Imports(m)
	if err != nil {
		t.Fatal(err)
	}
	sample := Module{
		Path: "example.com/sample", Name: "sample", Dir: root,
		Files: []ModuleFile{{Path: filepath.Join(root, "shapes.go"), Header: lspRange(0, 8, 14)}},
	}
	wantImports := []Import{
		{Module: sample, Weight: 1},
		{Module: Module{Path: "fmt", Name: "fmt"}, Weight: 1},
		{Module: Module{Path: "strings", Name: "strings"}, Weight: 1},
	}
	if !reflect.DeepEqual(imports, wantImports) {
		t.Errorf("Imports =\n %+v\nwant\n %+v", imports, wantImports)
	}
}

func TestModuleOfPackage(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"go.mod": "// a comment\nmodule \"example.com/m\" // quoted\n\ngo 1.26\n",
		// Several files: tests are left out; imports are counted per file.
		"lib/b.go":        "package lib\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nvar _ = fmt.Sprint(os.Args)\n",
		"lib/a.go":        "// Package lib is a library.\npackage lib\n\nimport \"fmt\"\nimport f \"fmt\"\nimport \"example.com/m/util/v2\"\n\nvar _, _ = fmt.Sprint(), f.Sprint()\n",
		"lib/a_test.go":   "package lib\n\nimport \"testing\"\n",
		"lib/notes.txt":   "not Go",
		"util/v2/util.go": "package util\n",
		// Non-ASCII before the package name: the header is in UTF-16 units.
		"wide/w.go": "/* é😀 */ package wide\n",
	})
	lib, ok, err := golang.ModuleOf(filepath.Join(dir, "lib", "a.go"))
	if err != nil || !ok {
		t.Fatalf("ModuleOf = %v, %v", ok, err)
	}
	if lib.Path != "example.com/m/lib" || lib.Name != "lib" || len(lib.Files) != 2 ||
		lib.Files[0].Path != filepath.Join(dir, "lib", "a.go") || lib.Files[0].Header != lspRange(1, 8, 11) {
		t.Errorf("lib = %+v", lib)
	}
	imports, err := golang.Imports(lib)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, imp := range imports {
		got = append(got, imp.Module.Path+" "+imp.Module.Name+" "+itoa(imp.Weight)+" "+filepath.ToSlash(rel(t, dir, imp.Module.Dir)))
	}
	want := []string{"example.com/m/util/v2 util 1 util/v2", "fmt fmt 2 -", "os os 1 -"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("imports = %q, want %q", got, want)
	}

	wide, ok, err := golang.ModuleOf(filepath.Join(dir, "wide", "w.go"))
	if err != nil || !ok {
		t.Fatalf("ModuleOf(wide) = %v, %v", ok, err)
	}
	// "/* é😀 */ package " is 17 UTF-16 units: é is one, 😀 two.
	if h := wide.Files[0].Header; h != lspRange(0, 18, 22) {
		t.Errorf("wide header = %+v", h)
	}
}

func rel(t *testing.T, dir, p string) string {
	if p == "" {
		return "-"
	}
	r, err := filepath.Rel(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func itoa(i int) string { return string(rune('0' + i)) }

func TestModuleOfNoPackage(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"go.mod":          "module example.com/m\n",
		"empty/x_test.go": "package empty\n",
	})
	if _, ok, err := golang.ModuleOf(filepath.Join(dir, "empty", "x_test.go")); ok || err != nil {
		t.Errorf("only tests: ok=%v err=%v, want no package", ok, err)
	}
	if _, ok, err := golang.ModuleOf(filepath.Join(dir, "missing", "x.go")); ok || err != nil {
		t.Errorf("missing directory: ok=%v err=%v, want no package", ok, err)
	}
	outside := t.TempDir() // no go.mod above it (the temp root has none)
	writeTree(t, outside, map[string]string{"x.go": "package x\n"})
	if _, ok, err := golang.ModuleOf(filepath.Join(outside, "x.go")); ok || err != nil {
		t.Errorf("no go.mod: ok=%v err=%v, want no package", ok, err)
	}
	if imports, err := golang.Imports(Module{Path: "fmt", Name: "fmt"}); imports != nil || err != nil {
		t.Errorf("Imports of a package not on disk = %v, %v; want none", imports, err)
	}
}

func TestModuleOfBrokenFile(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"go.mod": "module example.com/m\n", "x.go": "not go at all\n"})
	if _, _, err := golang.ModuleOf(filepath.Join(dir, "x.go")); err == nil {
		t.Error("a file without a package clause should be an error")
	}
}

func TestModulePath(t *testing.T) {
	tests := map[string]string{
		"module example.com/m\n":             "example.com/m",
		"go 1.26\nmodule   a/b // note\n":    "a/b",
		"module \"q/r\"\n":                   "q/r",
		"// module commented/out\ngo 1.26\n": "",
		"modulex y\nmodule\n":                "",
	}
	for in, want := range tests {
		if got := modulePath([]byte(in)); got != want {
			t.Errorf("modulePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPackageName(t *testing.T) {
	tests := map[string]string{
		"fmt": "fmt", "net/http": "http", "example.com/x/v2": "x", "v2": "v2", "gopkg.in/yaml.v3": "yaml.v3",
	}
	for in, want := range tests {
		if got := packageName(in); got != want {
			t.Errorf("packageName(%q) = %q, want %q", in, got, want)
		}
	}
}
