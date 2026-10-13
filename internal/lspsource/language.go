package lspsource

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// Language is what the source needs to know about a programming language
// beyond what its language server answers. A language is composed of the
// small parts below: it reuses the generic ones that fit and supplies its
// own for the rest.
type Language interface {
	Naming
	Declarations
	HoverReader
	Modules
}

// Naming reads symbol names from the document outline.
type Naming interface {
	// SplitName turns an outline name into the symbol's short name and,
	// when the outline name carries one, its qualified name.
	SplitName(name string) (short, qualified string)
	// Owner names the type that a top-level outline symbol is a member
	// of, for languages that declare members outside their type, and
	// returns "" otherwise. Members nested in the outline need no owner.
	Owner(name string) string
}

// Declarations corrects the declaration ranges a server reports.
type Declarations interface {
	// DeclarationStart returns where a declaration starts, given the text
	// of the line the server says it starts on and that start.
	DeclarationStart(line string, start lsp.Position) lsp.Position
}

// HoverReader splits hover Markdown into a signature and a doc.
type HoverReader interface {
	ParseHover(md string) (signature, doc string)
}

// Modules finds the module a file belongs to and what a module imports.
// A module is the unit of code that imports name: a package, a crate, a
// file of a scripting language.
type Modules interface {
	// ModuleOf returns the module of the source file at path. ok is false
	// when the language has no modules the source can find.
	ModuleOf(path string) (m Module, ok bool, err error)
	// Imports returns the modules that m imports, each once, in the order
	// of their paths.
	Imports(m Module) ([]Import, error)
}

// Module is a unit of code that other code imports by Path.
type Module struct {
	Path  string       // what importers write, e.g. "example.com/sample/report"
	Name  string       // how its code refers to it, e.g. "report"
	Dir   string       // absolute directory of its files; "" when not on disk
	Files []ModuleFile // its source files, sorted by path
}

// ModuleFile is one source file of a module. Header is the module's name
// in the file's module declaration (a package clause, say), in protocol
// coordinates.
type ModuleFile struct {
	Path   string
	Header lsp.Range
}

// Import is a module imported by another. Weight counts the importing
// files.
type Import struct {
	Module Module
	Weight int
}

// languageOf returns the language of the document uri.
func languageOf(uri string) Language {
	path, err := lsp.URIToPath(uri)
	if err != nil {
		return generic
	}
	switch lsp.LanguageID(filepath.Base(path)) {
	case "go":
		return golang
	default:
		return generic
	}
}

// genericLanguage relies on the outline and hover alone.
type genericLanguage struct {
	plainNames
	asDeclared
	markdownHover
	noModules
}

var generic Language = genericLanguage{}

// plainNames takes outline names as they are; members are nested in the
// outline under their type.
type plainNames struct{}

func (plainNames) SplitName(name string) (short, qualified string) { return name, "" }
func (plainNames) Owner(string) string                             { return "" }

// asDeclared trusts the server's declaration ranges.
type asDeclared struct{}

func (asDeclared) DeclarationStart(_ string, start lsp.Position) lsp.Position { return start }

// markdownHover takes the first code block of a hover as the signature and
// the prose after it, without horizontal rules, as the doc.
type markdownHover struct{}

func (markdownHover) ParseHover(md string) (signature, doc string) {
	code, rest, _ := firstCodeBlock(md)
	return strings.TrimSpace(code), paragraphs(rest, nil)
}

// noModules finds no modules.
type noModules struct{}

func (noModules) ModuleOf(string) (Module, bool, error) { return Module{}, false, nil }
func (noModules) Imports(Module) ([]Import, error)      { return nil, nil }

// firstCodeBlock splits hover Markdown that opens with a fenced code block
// into that block's code and the Markdown after it. Anything else is all
// rest, and ok is false.
func firstCodeBlock(md string) (code, rest string, ok bool) {
	md = strings.TrimSpace(md)
	if strings.HasPrefix(md, "```") {
		body := md[strings.IndexByte(md+"\n", '\n')+1:]
		if end := strings.Index(body, "```"); end >= 0 {
			return body[:end], body[end+3:], true
		}
	}
	return "", md, false
}

// paragraphs joins the non-empty paragraphs of md that are not horizontal
// rules and that drop (when set) does not reject.
func paragraphs(md string, drop func(string) bool) string {
	var kept []string
	for _, p := range strings.Split(strings.TrimSpace(md), "\n\n") {
		p = strings.TrimSpace(p)
		if p == "" || p == "---" || drop != nil && drop(p) {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, "\n\n")
}

var linkOnly = regexp.MustCompile("^\\[[^\\]]*\\]\\([^)]*\\)$")
