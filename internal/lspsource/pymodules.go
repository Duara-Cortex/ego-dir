package lspsource

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// pyModules finds Python modules on disk. Every .py file is a module. Its
// dotted path starts at the nearest directory above it without an
// __init__.py (the import root), so a/b/c.py is "a.b.c" when a and b are
// packages, and a/__init__.py is "a". The module's header is the first
// line of its docstring, which also gives its doc. Import statements are
// read with a scanner that knows comments, strings, brackets and line
// continuations, not with a parser.
type pyModules struct{}

// ModuleOf returns the module of the Python file at path.
func (pyModules) ModuleOf(path string) (Module, bool, error) {
	if !strings.HasSuffix(path, ".py") {
		return Module{}, false, nil
	}
	src, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Module{}, false, nil
	}
	if err != nil {
		return Module{}, false, err
	}
	modPath, _ := pyModulePath(path)
	scanned := scanPython(string(src))
	return Module{
		Path:      modPath,
		Name:      modPath[strings.LastIndexByte(modPath, '.')+1:],
		Dir:       filepath.Dir(path),
		Files:     []ModuleFile{{Path: path, Header: scanned.header}},
		File:      path,
		Described: true,
		Signature: "module " + modPath,
		Doc:       scanned.doc,
	}, true, nil
}

// Imports returns the modules that m's file imports. A module found under
// m's import root is read from disk; any other (the standard library,
// installed packages) has only a path and a name.
func (pyModules) Imports(m Module) ([]Import, error) {
	if m.File == "" {
		return nil, nil
	}
	paths, err := pyImportPaths(m)
	if err != nil {
		return nil, err
	}
	_, importRoot := pyModulePath(m.File)
	out := make([]Import, 0, len(paths))
	for _, p := range paths {
		imported := Module{Path: p, Name: p[strings.LastIndexByte(p, '.')+1:]}
		if file := pyLocalFile(importRoot, p); file != "" {
			local, ok, err := python.ModuleOf(file)
			if err != nil {
				return nil, err
			}
			if ok {
				imported = local
			}
		}
		out = append(out, Import{Module: imported, Weight: 1})
	}
	return out, nil
}

// Companions returns the Python files under root that import the module
// of path, and, when one of them is a package's __init__.py (which may
// pass the names on), the files that import that package, and so on.
func (pyModules) Companions(path, root string) ([]string, error) {
	if root == "" {
		return nil, nil
	}
	target, ok, err := python.ModuleOf(path)
	if err != nil || !ok {
		return nil, err
	}
	importers := map[string][]Module{} // by imported path
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && outsideWorkspaceDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(p, ".py") {
			return nil
		}
		m, ok, err := python.ModuleOf(p)
		if err != nil || !ok {
			return err
		}
		paths, err := pyImportPaths(m)
		if err != nil {
			return err
		}
		for _, ip := range paths {
			importers[ip] = append(importers[ip], m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{filepath.Clean(path): true}
	var out []string
	for queue := []string{target.Path}; len(queue) > 0; queue = queue[1:] {
		for _, m := range importers[queue[0]] {
			if seen[filepath.Clean(m.File)] {
				continue
			}
			seen[filepath.Clean(m.File)] = true
			out = append(out, m.File)
			if filepath.Base(m.File) == "__init__.py" {
				queue = append(queue, m.Path)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// pyModulePath returns the dotted path of the module in the file at path
// and its import root.
func pyModulePath(path string) (modPath, importRoot string) {
	dir := filepath.Dir(path)
	var parts []string
	if base := filepath.Base(path); base != "__init__.py" {
		parts = []string{strings.TrimSuffix(base, ".py")}
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "__init__.py")); err != nil {
			break
		}
		parts = append([]string{filepath.Base(dir)}, parts...)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return strings.Join(parts, "."), dir
}

// pyLocalFile returns the file of the module at the dotted path under
// importRoot, or "" when there is none.
func pyLocalFile(importRoot, dotted string) string {
	base := filepath.Join(importRoot, filepath.FromSlash(strings.ReplaceAll(dotted, ".", "/")))
	for _, p := range []string{base + ".py", filepath.Join(base, "__init__.py")} {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// pyImportPaths returns the dotted paths m's file imports, each once,
// sorted. "import a.b" imports a.b. "from a import b" imports a.b when
// that is a module under the import root, and a otherwise. A relative
// import is resolved against m's package; one that climbs out of it is
// left out.
func pyImportPaths(m Module) ([]string, error) {
	src, err := os.ReadFile(m.File)
	if err != nil {
		return nil, err
	}
	_, importRoot := pyModulePath(m.File)
	pkg := strings.Split(m.Path, ".")
	if filepath.Base(m.File) != "__init__.py" {
		pkg = pkg[:len(pkg)-1]
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, imp := range scanPython(string(src)).imports {
		from := imp.module
		if imp.level > 0 {
			if imp.level-1 > len(pkg) {
				continue
			}
			from = strings.Join(append(append([]string(nil), pkg[:len(pkg)-(imp.level-1)]...), splitDotted(imp.module)...), ".")
			if from == "" && len(imp.names) == 0 {
				continue
			}
		}
		if imp.names == nil {
			add(from)
			continue
		}
		for _, name := range imp.names {
			sub := name
			if from != "" {
				sub = from + "." + name
			}
			if name != "*" && pyLocalFile(importRoot, sub) != "" {
				add(sub)
			} else {
				add(from)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func splitDotted(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ".")
}

// outsideWorkspaceDir reports whether a directory of this name, below the
// workspace root, holds code that is not the workspace's own: installed
// packages, caches and hidden directories (a virtual environment such as
// .venv, version control).
func outsideWorkspaceDir(name string) bool {
	switch name {
	case "node_modules", "site-packages", "dist-packages", "__pycache__":
		return true
	}
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}

// pyImport is one import statement: "from ..a import b, c" has level 2,
// module "a" and names [b c]; "import a.b" has module "a.b" and no names.
type pyImport struct {
	level  int
	module string
	names  []string
}

// pyScan is what ego-dir reads from a Python file: the module docstring
// (cleaned as inspect.cleandoc does), the range of its first line, and
// the import statements.
type pyScan struct {
	doc     string
	header  lsp.Range // empty when there is no docstring
	imports []pyImport
}

// scanPython reads src statement by statement. Strings become a
// placeholder in the statement's text, comments are dropped, and
// brackets and backslashes join lines.
func scanPython(src string) pyScan {
	var out pyScan
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var stmt strings.Builder
	first := true // no statement has ended yet
	var str struct {
		text      string
		line, col int // where the first string of the statement starts
		count     int
	}
	end := func() {
		text := strings.TrimSpace(stmt.String())
		stmt.Reset()
		if text == "" {
			return
		}
		if first {
			first = false
			if str.count == 1 && strings.TrimLeft(text, "rRuU") == `""` {
				out.doc = cleanDoc(str.text)
				lineText := lines[str.line]
				out.header = lsp.Range{
					Start: lsp.Position{Line: str.line, Character: utf16Len(lineText[:str.col])},
					End:   lsp.Position{Line: str.line, Character: utf16Len(lineText)},
				}
			}
		}
		str.count = 0
		out.imports = append(out.imports, parseImport(text)...)
	}
	depth := 0
	line, col := 0, 0 // of src[i]
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case c == '\'' || c == '"':
			startLine, startCol := line, col
			// A prefix such as r or b is already in the statement.
			if p := strings.TrimRight(stmt.String(), "rRbBuUfF"); len(p) < stmt.Len() {
				startCol -= stmt.Len() - len(p)
			}
			quote := string(c)
			if strings.HasPrefix(src[i:], strings.Repeat(quote, 3)) {
				quote = strings.Repeat(quote, 3)
			}
			j := i + len(quote)
			closed := false
			for j < len(src) {
				if strings.HasPrefix(src[j:], quote) {
					closed = true
					break
				}
				if src[j] == '\n' && len(quote) == 1 {
					break // unterminated: the line ends the string
				}
				if src[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j, len(src))
			body := src[i+len(quote) : j]
			if str.count == 0 {
				str.text, str.line, str.col = body, startLine, max(startCol, 0)
			}
			str.count++
			stmt.WriteString(`""`)
			if closed {
				j += len(quote)
			}
			for _, r := range src[i:j] {
				if r == '\n' {
					line, col = line+1, 0
				} else {
					col += len(string(r))
				}
			}
			i = j
			continue
		case c == '\\' && i+1 < len(src) && src[i+1] == '\n':
			stmt.WriteByte(' ')
			i, line, col = i+2, line+1, 0
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth = max(depth-1, 0)
		case c == '\n':
			if depth == 0 {
				end()
			} else {
				stmt.WriteByte(' ')
			}
			i, line, col = i+1, line+1, 0
			continue
		case c == ';' && depth == 0:
			end()
			i, col = i+1, col+1
			continue
		}
		stmt.WriteByte(c)
		i, col = i+1, col+1
	}
	end()
	return out
}

// parseImport reads the imports of a statement's text: "import a, b.c"
// is two, a "from" statement one, anything else none.
func parseImport(text string) []pyImport {
	if rest, ok := strings.CutPrefix(text, "import "); ok {
		var out []pyImport
		for _, part := range strings.Split(rest, ",") {
			if f := strings.Fields(part); len(f) > 0 {
				out = append(out, pyImport{module: f[0]})
			}
		}
		return out
	}
	rest, ok := strings.CutPrefix(text, "from ")
	if !ok {
		return nil
	}
	module, names, ok := strings.Cut(rest, " import ")
	if !ok {
		return nil
	}
	module = strings.TrimSpace(module)
	imp := pyImport{module: strings.TrimLeft(module, "."), names: []string{}}
	imp.level = len(module) - len(imp.module)
	names = strings.NewReplacer("(", " ", ")", " ").Replace(names)
	for _, part := range strings.Split(names, ",") {
		if f := strings.Fields(part); len(f) > 0 {
			imp.names = append(imp.names, f[0])
		}
	}
	return []pyImport{imp}
}

// cleanDoc cleans a docstring as Python's inspect.cleandoc does: the first
// line is stripped, the common indentation of the others removed, and
// blank lines at either end dropped.
func cleanDoc(doc string) string {
	lines := strings.Split(strings.ReplaceAll(doc, "\t", "        "), "\n")
	indent := -1
	for _, l := range lines[1:] {
		if t := strings.TrimLeft(l, " "); t != "" && (indent < 0 || len(l)-len(t) < indent) {
			indent = len(l) - len(t)
		}
	}
	lines[0] = strings.TrimSpace(lines[0])
	for i := 1; i < len(lines); i++ {
		if indent > 0 && len(lines[i]) >= indent {
			lines[i] = lines[i][indent:]
		}
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

// utf16Len is the length of s in UTF-16 code units.
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }
