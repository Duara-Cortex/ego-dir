package lspsource

import (
	"html"
	"regexp"
	"strings"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// pythonLanguage is Python as pyright presents it. Members are nested in
// the outline and declarations start at their keyword or decorator, as
// the generic parts expect.
type pythonLanguage struct {
	plainNames
	asDeclared
	pyrightHover
	pyModules
	pyOutline
}

var python Language = pythonLanguage{}

// pyOutline drops what pyright lists inside a function (its parameters
// and local variables) and makes a variable of a class (an attribute,
// such as one assigned to self.w) a field.
type pyOutline struct{}

func (pyOutline) OutlineEntry(sym lsp.DocumentSymbol, parent *lsp.DocumentSymbol) (lsp.DocumentSymbol, bool) {
	if sym.Kind != lsp.SymbolKindVariable || parent == nil {
		return sym, true
	}
	switch parent.Kind {
	case lsp.SymbolKindFunction, lsp.SymbolKindMethod, lsp.SymbolKindConstructor:
		return sym, false
	case lsp.SymbolKindClass:
		sym.Kind = lsp.SymbolKindField
	}
	return sym, true
}

// pyrightHover reads pyright hover text: a code block whose first word in
// parentheses names what the symbol is, "(function) def f(...)", laid out
// over several lines when it has more than one parameter, then a rule on
// the next line and the docstring, escaped for Markdown.
type pyrightHover struct{}

var (
	// hoverCategory is how pyright opens a signature: "(function) ",
	// "(method) ", "(variable) ", "(module) " and the like.
	hoverCategory = regexp.MustCompile(`^\([a-z ]+\) `)
	// markdownEscape is a character pyright escaped with a backslash.
	markdownEscape = regexp.MustCompile("\\\\([\\\\`*_{}\\[\\]()#+\\-.!|<>~])")
)

func (pyrightHover) ParseHover(md string) (signature, doc string) {
	code, rest, ok := firstCodeBlock(md)
	if ok {
		signature = oneLine(hoverCategory.ReplaceAllString(strings.TrimSpace(code), ""))
	}
	return signature, unescapeMarkdown(paragraphs(ruleLinesAsBreaks(rest), nil))
}

// oneLine joins a signature pyright laid out over several lines:
// "def f(\n    a: int,\n    b: int\n) -> int" is "def f(a: int, b: int) -> int".
func oneLine(sig string) string {
	var b strings.Builder
	for i, line := range strings.Split(sig, "\n") {
		line = strings.TrimSpace(line)
		if i > 0 && !strings.HasSuffix(b.String(), "(") && !strings.HasPrefix(line, ")") {
			b.WriteByte(' ')
		}
		b.WriteString(line)
	}
	return b.String()
}

// ruleLinesAsBreaks turns every line that is only a horizontal rule into a
// paragraph break: pyright writes the rule on the line after the code
// block, with no blank line around it.
func ruleLinesAsBreaks(md string) string {
	lines := strings.Split(md, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			lines[i] = "\n"
		}
	}
	return strings.Join(lines, "\n")
}

// unescapeMarkdown undoes the escaping pyright applies to a docstring:
// backslashes before Markdown characters, HTML entities (&nbsp; for
// indentation) and the two spaces it ends a line with to keep the break.
func unescapeMarkdown(s string) string {
	s = markdownEscape.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(html.UnescapeString(s), " ", " ")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}
