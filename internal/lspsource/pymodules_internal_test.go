package lspsource

import (
	"path/filepath"
	"reflect"
	"testing"
)

func pythonRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "python"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestModuleOfPythonFixture(t *testing.T) {
	root := pythonRoot(t)
	shapes := filepath.Join(root, "shapes.py")
	pkg := filepath.Join(root, "report", "__init__.py")
	report := filepath.Join(root, "report", "report.py")
	module := func(path, modPath, name, dir string, header int, doc string) Module {
		return Module{Path: modPath, Name: name, Dir: dir, File: path,
			Files:     []ModuleFile{{Path: path, Header: lspRange(0, 0, header)}},
			Described: true, Signature: "module " + modPath, Doc: doc}
	}
	tests := []struct {
		path string
		want Module
	}{
		{shapes, module(shapes, "shapes", "shapes", root, 29, "Shapes and their areas.")},
		{pkg, module(pkg, "report", "report", filepath.Join(root, "report"), 35, "Describe the sample's shapes.")},
		{report, module(report, "report.report", "report", filepath.Join(root, "report"), 31, "Format shapes for people.")},
	}
	for _, tc := range tests {
		m, ok, err := python.ModuleOf(tc.path)
		if err != nil || !ok {
			t.Fatalf("ModuleOf(%s) = %v, %v", tc.path, ok, err)
		}
		if !reflect.DeepEqual(m, tc.want) {
			t.Errorf("ModuleOf(%s) =\n %+v\nwant\n %+v", tc.path, m, tc.want)
		}
	}

	m, _, _ := python.ModuleOf(report)
	imports, err := python.Imports(m)
	if err != nil {
		t.Fatal(err)
	}
	shapesModule, _, _ := python.ModuleOf(shapes)
	want := []Import{{Module: shapesModule, Weight: 1}, {Module: Module{Path: "string", Name: "string"}, Weight: 1}}
	if !reflect.DeepEqual(imports, want) {
		t.Errorf("Imports(report.report) =\n %+v\nwant\n %+v", imports, want)
	}
	if imports, err := python.Imports(shapesModule); err != nil || !reflect.DeepEqual(imports, []Import{{Module: Module{Path: "abc", Name: "abc"}, Weight: 1}}) {
		t.Errorf("Imports(shapes) = %+v, %v; want abc", imports, err)
	}
	// A module known only by its path imports nothing.
	if imports, err := python.Imports(Module{Path: "string", Name: "string"}); imports != nil || err != nil {
		t.Errorf("Imports(string) = %v, %v", imports, err)
	}
}

func TestModuleOfNonPython(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"notes.txt": "x"})
	for _, p := range []string{filepath.Join(dir, "notes.txt"), filepath.Join(dir, "missing.py")} {
		if _, ok, err := python.ModuleOf(p); ok || err != nil {
			t.Errorf("ModuleOf(%s) = %v, %v; want no module", p, ok, err)
		}
	}
}

func TestPyModulePath(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"top.py":                   "",
		"src/app/__init__.py":      "",
		"src/app/core/__init__.py": "",
		"src/app/core/models.py":   "",
		"loose/script.py":          "",
	})
	tests := []struct{ file, path, root string }{
		{"top.py", "top", "."},
		{"src/app/__init__.py", "app", "src"},
		{"src/app/core/models.py", "app.core.models", "src"},
		{"src/app/core/__init__.py", "app.core", "src"},
		{"loose/script.py", "script", "loose"}, // no __init__.py: loose is not a package
	}
	for _, tc := range tests {
		p, r := pyModulePath(filepath.Join(dir, filepath.FromSlash(tc.file)))
		if p != tc.path || r != filepath.Join(dir, filepath.FromSlash(tc.root)) {
			t.Errorf("pyModulePath(%s) = %q, %q; want %q, %q", tc.file, p, r, tc.path, tc.root)
		}
	}
}

func TestPyImportPaths(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"pkg/__init__.py":       "",
		"pkg/sub.py":            "",
		"pkg/inner/__init__.py": "",
		"pkg/inner/deep.py":     "",
		"other.py":              "",
	})
	tests := []struct {
		name, src string
		want      []string
	}{
		{"plain", "import os\n", []string{"os"}},
		{"dotted, aliased, several", "import os.path as p, sys\n", []string{"os.path", "sys"}},
		{"from a module", "from collections import OrderedDict, deque\n", []string{"collections"}},
		{"from a package, a submodule", "from pkg import sub, VALUE\n", []string{"pkg", "pkg.sub"}},
		{"star", "from pkg import *\n", []string{"pkg"}},
		{"relative to the package", "from . import sub\nfrom .sub import f\n", []string{"pkg.inner", "pkg.inner.sub"}},
		{"two levels up", "from .. import sub\n", []string{"pkg.sub"}},
		{"climbing out of the packages", "from .... import x\n", nil},
		{"parenthesised over lines", "from pkg import (\n    sub,  # the submodule\n    VALUE,\n)\n", []string{"pkg", "pkg.sub"}},
		{"backslash continuation", "import os, \\\n    sys\n", []string{"os", "sys"}},
		{"semicolons", "import os; import sys\n", []string{"os", "sys"}},
		{"indented, under a condition", "if TYPE_CHECKING:\n    from typing import Any\n", []string{"typing"}},
		{"in a comment", "# import nope\nx = 1  # from nope import x\n", nil},
		{"in strings", "s = 'import nope'\nt = \"\"\"\nimport nope\nfrom nope import x\n\"\"\"\n", nil},
		{"each once", "import os\nimport os\nfrom os import path\n", []string{"os"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The importing file is pkg/inner/deep.py, module pkg.inner.deep.
			path := filepath.Join(dir, "pkg", "inner", "deep.py")
			writeTree(t, dir, map[string]string{"pkg/inner/deep.py": tc.src})
			m, ok, err := python.ModuleOf(path)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			got, err := pyImportPaths(m)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("imports of %q = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

func TestScanPythonDocstring(t *testing.T) {
	tests := []struct {
		name, src string
		doc       string
		header    [3]int // line, start, end; all zero for none
	}{
		{"one line", "\"\"\"Shapes.\"\"\"\nimport os\n", "Shapes.", [3]int{0, 0, 13}},
		{"after comments and blank lines", "#!/usr/bin/env python3\n# A comment.\n\n'''Tool.'''\n", "Tool.", [3]int{3, 0, 11}},
		{"raw", "r\"\"\"Raw \\d.\"\"\"\n", "Raw \\d.", [3]int{0, 0, 14}},
		{"over several lines, cleaned", "\"\"\"Summary.\n\n    Details,\n      indented.\n    \"\"\"\n", "Summary.\n\nDetails,\n  indented.", [3]int{0, 0, 11}},
		{"single quotes", "'Short.'\n", "Short.", [3]int{0, 0, 8}},
		{"UTF-16 columns", "\"\"\"Clef 𝄞.\"\"\"\n", "Clef 𝄞.", [3]int{0, 0, 14}},
		{"none: code first", "import os\n\"\"\"Not a docstring.\"\"\"\n", "", [3]int{}},
		{"none: an assignment", "X = \"\"\"Not one either.\"\"\"\n", "", [3]int{}},
		{"none: an f-string", "f\"\"\"{x}\"\"\"\n", "", [3]int{}},
		{"none: a bytes literal", "b\"\"\"Bytes.\"\"\"\n", "", [3]int{}},
		{"none: an empty file", "", "", [3]int{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scanPython(tc.src)
			want := lspRange(tc.header[0], tc.header[1], tc.header[2])
			if got.doc != tc.doc || got.header != want {
				t.Errorf("scanPython(%q) = doc %q, header %+v; want %q, %+v", tc.src, got.doc, got.header, tc.doc, want)
			}
		})
	}
}

func TestScanPythonSurvivesBrokenSource(t *testing.T) {
	for _, src := range []string{"'unterminated\nimport os\n", "\"\"\"never closed\nimport os\n", "f(\nimport os\n", "x = ]]]\nimport os\n"} {
		scanPython(src) // must not panic
	}
	if got := scanPython("'unterminated\nimport os\n").imports; !reflect.DeepEqual(got, []pyImport{{module: "os"}}) {
		t.Errorf("an unterminated string ends with its line: imports = %+v", got)
	}
}

func TestPyCompanions(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"shapes.py":                         "class Rect: ...\n",
		"direct.py":                         "import shapes\n",
		"pkg/__init__.py":                   "from shapes import Rect\n",
		"pkg/api.py":                        "",
		"through_package.py":                "from pkg import Rect\n",
		"unrelated.py":                      "import os\n",
		".venv/lib/site-packages/vendor.py": "import shapes\n",
		"node_modules/tool.py":              "import shapes\n",
		"build/__pycache__/cached.py":       "import shapes\n",
	})
	got, err := python.Companions(filepath.Join(dir, "shapes.py"), dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "direct.py"), filepath.Join(dir, "pkg", "__init__.py"), filepath.Join(dir, "through_package.py")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Companions =\n %q\nwant\n %q", got, want)
	}

	root := pythonRoot(t)
	got, err = python.Companions(filepath.Join(root, "shapes.py"), root)
	if err != nil || !reflect.DeepEqual(got, []string{filepath.Join(root, "report", "report.py")}) {
		t.Errorf("Companions(shapes.py) in the fixture = %q, %v; want report/report.py", got, err)
	}
	if got, err := python.Companions(filepath.Join(root, "shapes.py"), ""); got != nil || err != nil {
		t.Errorf("Companions without a root = %q, %v; want none", got, err)
	}
	for _, lang := range []Language{golang, generic} {
		if got, err := lang.Companions(filepath.Join(root, "shapes.py"), root); got != nil || err != nil {
			t.Errorf("%T: Companions = %q, %v; want none", lang, got, err)
		}
	}
}

func TestOutsideWorkspaceDir(t *testing.T) {
	for name, want := range map[string]bool{
		".venv": true, ".git": true, "site-packages": true, "dist-packages": true, "node_modules": true, "__pycache__": true,
		"report": false, "src": false, "_internal": false, ".": false, "..": false,
	} {
		if got := outsideWorkspaceDir(name); got != want {
			t.Errorf("outsideWorkspaceDir(%q) = %v, want %v", name, got, want)
		}
	}
}
