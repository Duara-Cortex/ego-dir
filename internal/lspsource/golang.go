package lspsource

import (
	"regexp"
	"strings"

	"github.com/Duara-Cortex/ego-dir/internal/lsp"
)

// goLanguage is Go as gopls presents it.
type goLanguage struct {
	receiverNames
	typeKeyword
	goplsHover
	goPackages
}

var golang Language = goLanguage{}

// receiverNames reads gopls outline names: a method is declared outside
// its type and named "(T).M" or "(*T).M".
type receiverNames struct{}

var receiverName = regexp.MustCompile(`^\(\*?([^()]+)\)\.(.+)$`)

// SplitName turns "(T).M" or "(*T).M" into "M" and the qualified "T.M".
func (receiverNames) SplitName(name string) (short, qualified string) {
	if m := receiverName.FindStringSubmatch(name); m != nil {
		return m[2], m[1] + "." + m[2]
	}
	return name, ""
}

// Owner returns the receiver type T of "(T).M" or "(*T).M".
func (receiverNames) Owner(name string) string {
	if m := receiverName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// typeKeyword widens a type declaration over its "type" keyword: gopls
// starts a type's range at its name, so the body would otherwise read
// "Shape interface {...}".
type typeKeyword struct{}

// DeclarationStart moves start back over a "type" keyword that is alone
// before it on its line.
func (typeKeyword) DeclarationStart(line string, start lsp.Position) lsp.Position {
	_, col := lsp.PositionToEditor(line, start)
	runes := []rune(line)
	if col-1 > len(runes) {
		return start
	}
	before := string(runes[:col-1])
	if strings.TrimSpace(before) != "type" {
		return start
	}
	// Only ASCII precedes the name, so runes and UTF-16 units agree.
	return lsp.Position{Line: start.Line, Character: strings.Index(before, "type")}
}

// goplsHover reads gopls hover text, dropping what gopls adds around the
// signature and doc: memory layout comments, the method set code block,
// the link to pkg.go.dev and, for a package, the list of its path, module
// and Go version.
type goplsHover struct{}

var (
	// laterCode is a fenced code block after the signature: gopls lists a
	// type's method set there.
	laterCode = regexp.MustCompile("(?s)```.*?```")
	// layout is the memory layout gopls appends to a type or field, e.g.
	// " // size=200 (0xc8), class=208 (0xd0)", "..., offset=16 (0x10)" or,
	// for small values, " // size=8, offset=0".
	layout = regexp.MustCompile(`(?m)[ \t]*// (?:(?:size|class|offset)=\d+(?: \(0x[0-9a-f]+\))?(?:, |$))+$`)
	// packageFacts is the list gopls ends a package's hover with.
	packageFacts = regexp.MustCompile(`^(?:[ \t]*- (?:Package path|Module|Language version): .*(?:\n|$))+$`)
)

// goplsDecoration reports whether a hover paragraph is one gopls adds.
func goplsDecoration(p string) bool {
	return linkOnly.MatchString(p) || packageFacts.MatchString(p)
}

func (goplsHover) ParseHover(md string) (signature, doc string) {
	code, rest, ok := firstCodeBlock(md)
	if !ok {
		return "", paragraphs(rest, goplsDecoration)
	}
	signature = strings.TrimSpace(layout.ReplaceAllString(code, ""))
	return signature, paragraphs(laterCode.ReplaceAllString(rest, ""), goplsDecoration)
}
