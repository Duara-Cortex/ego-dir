package lspsource

import (
	"bufio"
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// goPackages finds Go packages on disk. A package is the .go files of one
// directory, without tests; its import path is the module path in the
// nearest go.mod followed by the directory below it. Build constraints are
// not evaluated: every file counts.
type goPackages struct{}

// ModuleOf returns the package of the Go file at path.
func (goPackages) ModuleOf(path string) (Module, bool, error) {
	return loadPackage(filepath.Dir(path))
}

// Imports returns the packages imported by m's files. Packages of m's own
// Go module are found on disk; others have only a path and a name. A
// package that is not on disk imports nothing.
func (goPackages) Imports(m Module) ([]Import, error) {
	if m.Dir == "" {
		return nil, nil
	}
	weights := map[string]int{}
	fset := token.NewFileSet()
	for _, f := range m.Files {
		parsed, err := parser.ParseFile(fset, f.Path, nil, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, spec := range parsed.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil || seen[p] {
				continue
			}
			seen[p] = true
			weights[p]++
		}
	}
	paths := make([]string, 0, len(weights))
	for p := range weights {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	modDir, modPath, found, err := goModule(m.Dir)
	if err != nil {
		return nil, err
	}
	out := make([]Import, 0, len(paths))
	for _, p := range paths {
		imported := Module{Path: p, Name: packageName(p)}
		if rest, ok := strings.CutPrefix(p, modPath); found && ok && (rest == "" || rest[0] == '/') {
			local, ok, err := loadPackage(filepath.Join(modDir, filepath.FromSlash(rest)))
			if err != nil {
				return nil, err
			}
			if ok {
				imported = local
			}
		}
		out = append(out, Import{Module: imported, Weight: weights[p]})
	}
	return out, nil
}

// loadPackage reads the package in dir. ok is false when dir has no Go
// files or is not inside a Go module.
func loadPackage(dir string) (m Module, ok bool, err error) {
	modDir, modPath, found, err := goModule(dir)
	if err != nil || !found {
		return Module{}, false, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Module{}, false, nil
	}
	if err != nil {
		return Module{}, false, err
	}
	rel, err := filepath.Rel(modDir, dir)
	if err != nil {
		return Module{}, false, err
	}
	m = Module{Path: modPath, Dir: dir}
	if rel != "." {
		m.Path += "/" + filepath.ToSlash(rel)
	}
	fset := token.NewFileSet()
	for _, e := range entries { // ReadDir sorts by name
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		p := filepath.Join(dir, name)
		src, err := os.ReadFile(p)
		if err != nil {
			return Module{}, false, err
		}
		parsed, err := parser.ParseFile(fset, p, src, parser.PackageClauseOnly)
		if err != nil {
			return Module{}, false, err
		}
		if m.Name == "" {
			m.Name = parsed.Name.Name
		}
		m.Files = append(m.Files, ModuleFile{Path: p, Header: identRange(src, fset.Position(parsed.Name.Pos()), parsed.Name.Name)})
	}
	if len(m.Files) == 0 {
		return Module{}, false, nil
	}
	return m, true, nil
}

// identRange converts the position of the identifier name (byte columns)
// into a protocol range (UTF-16 columns).
func identRange(src []byte, pos token.Position, name string) lsp.Range {
	lineStart := pos.Offset - (pos.Column - 1)
	before := string(src[lineStart:pos.Offset])
	end := bytes.IndexByte(src[lineStart:], '\n')
	if end < 0 {
		end = len(src) - lineStart
	}
	line := string(src[lineStart : lineStart+end])
	start, _ := lsp.EditorToPosition(line, pos.Line, utf8.RuneCountInString(before)+1)
	stop, _ := lsp.EditorToPosition(line, pos.Line, utf8.RuneCountInString(before)+utf8.RuneCountInString(name)+1)
	return lsp.Range{Start: start, End: stop}
}

// goModule finds the go.mod that governs dir, walking up the tree, and
// returns its directory and module path.
func goModule(dir string) (modDir, modPath string, found bool, err error) {
	for d := dir; ; {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		switch {
		case err == nil:
			p := modulePath(data)
			return d, p, p != "", nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", "", false, err
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", "", false, nil
		}
		d = parent
	}
}

// modulePath returns the path of the module directive in go.mod data, or
// "" when there is none.
func modulePath(data []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "//")
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "module" {
			continue
		}
		if p, err := strconv.Unquote(fields[1]); err == nil {
			return p
		}
		return fields[1]
	}
	return ""
}

var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

// packageName guesses the name of the package at an import path that is
// not on disk: its last element, skipping a major version suffix
// ("example.com/x/v2" is x).
func packageName(importPath string) string {
	name := path.Base(importPath)
	if dir := path.Dir(importPath); majorVersion.MatchString(name) && dir != "." {
		name = path.Base(dir)
	}
	return name
}
