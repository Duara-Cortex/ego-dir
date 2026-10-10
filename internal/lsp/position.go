package lsp

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// EditorToPosition converts an editor position (1-based line, 1-based column
// counted in characters) to a protocol Position. lineText is the text of
// that line, without its line ending. The column may be one past the last
// character (the end of the line).
func EditorToPosition(lineText string, line, col int) (Position, error) {
	if line < 1 || col < 1 {
		return Position{}, fmt.Errorf("position %d:%d: line and column start at 1", line, col)
	}
	runes := []rune(lineText)
	if col > len(runes)+1 {
		return Position{}, fmt.Errorf("position %d:%d: line %d has only %d characters", line, col, line, len(runes))
	}
	units := 0
	for _, r := range runes[:col-1] {
		units += utf16Len(r)
	}
	return Position{Line: line - 1, Character: units}, nil
}

// PositionToEditor converts a protocol Position on the line lineText to a
// 1-based editor line and column. A column inside a surrogate pair maps to
// the character it belongs to; one past the end of the line is clamped.
func PositionToEditor(lineText string, p Position) (line, col int) {
	units, chars := 0, 0
	for _, r := range lineText {
		if units+utf16Len(r) > p.Character {
			break
		}
		units += utf16Len(r)
		chars++
	}
	return p.Line + 1, chars + 1
}

func utf16Len(r rune) int {
	if r == utf8.RuneError {
		return 1
	}
	return len(utf16.Encode([]rune{r}))
}

// FileURI returns the file:// URI of path, made absolute first.
func FileURI(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive letter: C:/x -> /C:/x
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// URIToPath returns the file path of a file:// URI.
func URIToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse URI %q: %w", uri, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("URI %q is not a file URI", uri)
	}
	if u.Path == "" {
		return "", errors.New("file URI has no path")
	}
	p := u.Path
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p), nil
}
